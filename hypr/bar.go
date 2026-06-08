package hypr

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/kovidgoyal/kitty/tools/utils"
)

var _ = fmt.Print
var repr = utils.Repr
var _ = repr

func handle_bar_event(line string, set_strings func(...string)) (err error) {
	log.Printf("hypr: event: %s", line)
	which, payload, found := strings.Cut(line, ">>")
	if !found {
		err = fmt.Errorf("Invalid event from hyprland: %s", line)
		log.Printf("hypr: parse error: %v", err)
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
				log.Printf("hypr: pw-play failed: %v", err)
			}
		}()
	}
	return
}

func bar_loop(conn *net.UnixConn, set_strings func(...string)) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("hypr: panic in bar_loop: %v", r)
		}
	}()

	backoff := 100 * time.Millisecond
	const maxBackoff = 30 * time.Second
	currentConn := conn

	for {
		reader := bufio.NewReader(currentConn)
		log.Printf("hypr: events loop reading")
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("hypr: read error: %v", err)
			currentConn.Close()
			// Reconnect with exponential backoff. NEVER exit silently —
			// that was the pre-existing bug: a transient Hyprland restart
			// (common around sleep/resume) would orphan this goroutine
			// and freeze the workspace/title segment forever.
			for {
				newConn, rerr := GetEventsConnection()
				if rerr == nil {
					log.Printf("hypr: reconnected after %s", backoff)
					currentConn = newConn
					backoff = 100 * time.Millisecond
					break
				}
				log.Printf("hypr: reconnect failed: %v (retrying in %s)", rerr, backoff)
				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		// successful read: reset backoff so the *next* disconnect starts fresh
		backoff = 100 * time.Millisecond
		line = strings.TrimSpace(line)
		if err := handle_bar_event(line, set_strings); err != nil {
			log.Printf("hypr: handle event failed: %v", err)
		}
	}
}

func HyprBar(set_strings func(...string)) (err error) {
	log.Printf("hypr: initializing")
	defer func() {
		if r := recover(); r != nil {
			log.Printf("hypr: panic in HyprBar: %v", r)
		}
	}()
	var conn *net.UnixConn
	if conn, err = GetEventsConnection(); err != nil {
		log.Printf("hypr: get events connection failed: %v", err)
		return err
	}
	var activeworkspace Workspace
	var activewindow Window
	if err = make_requests(request{"activeworkspace", &activeworkspace}, request{"activewindow", &activewindow}); err != nil {
		log.Printf("hypr: initial requests failed: %v", err)
		conn.Close()
		return
	}
	log.Printf("hypr: initial state - title: %s, workspace: %s", activewindow.Title, activeworkspace.Name)
	set_strings("title:"+activewindow.Title, "workspace:"+activeworkspace.Name)
	go bar_loop(conn, set_strings)
	return
}
