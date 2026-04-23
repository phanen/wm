package hypr

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"

	"github.com/kovidgoyal/kitty/tools/utils"
)

var _ = fmt.Print
var repr = utils.Repr
var _ = repr

func handle_bar_event(line string, set_strings func(...string)) (err error) {
	log.Printf("Received event: %s", line)
	which, payload, found := strings.Cut(line, ">>")
	if !found {
		err = fmt.Errorf("Invalid event from hyprland: %s", line)
		log.Printf("Error: %v", err)
		return err
	}
	switch which {
	case "activewindow":
		_, title, found := strings.Cut(payload, ",")
		if found {
			set_strings("title:" + title)
		}
	case "workspace":
		set_strings("workspace:" + payload)
	case "focusedmon":
		_, name, found := strings.Cut(payload, ",")
		if found {
			set_strings("workspace:" + name)
		}
	case "bell":
		// See https://github.com/hyprwm/Hyprland/discussions/10428
		// Eventually use: https://specifications.freedesktop.org/sound-theme-spec/latest/sound_lookup.html
		// to avoid hardcoding sound file path.
		cmd := exec.Command("pw-play", "/usr/share/sounds/ocean/stereo/bell.oga")
		go func() {
			if err := cmd.Run(); err != nil {
				log.Printf("Failed to play bell: %v", err)
			}
		}()
	}
	return
}

func bar_loop(conn *net.UnixConn, set_strings func(...string)) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in bar_loop: %v", r)
		}
	}()
	reader := bufio.NewReader(conn)
	defer conn.Close()
	log.Printf("Started Hyprland events loop")
	for {
		if line, err := reader.ReadString('\n'); err != nil {
			log.Printf("Failed to read from Hyprland events socket: %v", err)
			var conn *net.UnixConn
			if conn, err = GetEventsConnection(); err != nil {
				log.Printf("Failed to reconnect to Hyprland events socket: %v", err)
				return
			}
			go bar_loop(conn, set_strings)
			return
		} else {
			line = strings.TrimSpace(line)
			if err := handle_bar_event(line, set_strings); err != nil {
				log.Printf("Failed to handle hyprland event: %s with error: %v", line, err)
			}
		}
	}
}

func HyprBar(set_strings func(...string)) (err error) {
	log.Printf("Initializing HyprBar")
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic in HyprBar: %v", r)
		}
	}()
	var conn *net.UnixConn
	if conn, err = GetEventsConnection(); err != nil {
		log.Printf("Failed to get events connection: %v", err)
		return err
	}
	var activeworkspace Workspace
	var activewindow Window
	if err = make_requests(request{"activeworkspace", &activeworkspace}, request{"activewindow", &activewindow}); err != nil {
		log.Printf("Failed to make initial requests: %v", err)
		conn.Close()
		return
	}
	log.Printf("Setting initial state - title: %s, workspace: %s", activewindow.Title, activeworkspace.Name)
	set_strings("title:"+activewindow.Title, "workspace:"+activeworkspace.Name)
	go bar_loop(conn, set_strings)
	return
}
