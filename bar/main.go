package bar

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"math"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"wm/hypr"
	"wm/sway"

	"github.com/kovidgoyal/kitty/tools/tty"
	"github.com/kovidgoyal/kitty/tools/tui/loop"
	"github.com/kovidgoyal/kitty/tools/utils"
	"github.com/kovidgoyal/kitty/tools/utils/style"
	"github.com/kovidgoyal/kitty/tools/wcswidth"
	"wm/model-quota"

	"golang.org/x/sys/unix"
)

var _ = fmt.Print
var debugprintln = tty.DebugPrintln

// ttyDebugWriter adapts tty.DebugPrintln to io.Writer so it can be used
// as a log sink. DebugPrintln is TUI-safe (no tty pollution), so this
// is the right fallback when syslog/journal isn't available (i.e. when
// ./wm bar is run directly in a terminal).
type ttyDebugWriter struct{}

func (ttyDebugWriter) Write(p []byte) (int, error) {
	tty.DebugPrintln(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

var DARK_GRAY, MEDIUM_GRAY, LIGHT_GRAY, GREEN, WHITE, BLACK, YELLOW, RED, ORANGE, DARK_ORANGE style.RGBA

const (
	// LEFT_DIVIDER  = ``
	// RIGHT_DIVIDER = ``
	LEFT_DIVIDER  = `|`
	RIGHT_DIVIDER = `|`
	// LEFT_END      = ``
	// RIGHT_END     = ``
	LEFT_END   = ``
	RIGHT_END  = ``
	VCS_SYMBOL = ``
	CLOCK      = `🕒`
	READONLY   = `🔒`
	BATTERY    = `🔋`
	CHARGING   = `🔌`
)

type network_load_data struct {
	time   time.Time
	rx, tx int64
}

type battery_data struct {
	power_now, energy_now, energy_full float64
	is_charging                        bool
	ratio                              float64
	hours, minutes                     int
}

type battery_history struct {
	charging, discharging []float64
}

type income_data struct {
	initialized bool
	val         string
	fg, bg      style.RGBA
}

type state struct {
	lp                            *loop.Loop
	update_timer                  loop.IdType
	now                           time.Time
	reported_failures             *utils.Set[string]
	num_cpus                      int
	network_load_data             map[string]network_load_data
	battery_history               map[string]*battery_history
	income_data                   income_data
	workspace_name, window_title  string
	wm_initialized                bool
	lock                          sync.Mutex
	prev_cpu_idle, prev_cpu_total uint64
}

func (s *state) report_failure(segment string, err error) {
	if s.reported_failures == nil {
		s.reported_failures = utils.NewSet[string]()
	}
	if !s.reported_failures.Has(segment) {
		s.reported_failures.Add(segment)
		log.Printf("The segment %s failed with error: %s", segment, err)
	}
}

type Segment struct {
	name, text string
	bg, fg     style.RGBA
	skip, bold bool
}

func color_as_sgr(self style.RGBA, is_fg bool) string {
	n := utils.IfElse(is_fg, 30, 40) + 8
	return fmt.Sprintf("%d:2:%d:%d:%d", n, self.Red, self.Green, self.Blue)
}

func (s Segment) styled_text() string {
	buf := strings.Builder{}
	if s.bold {
		buf.WriteString("\x1b[1m")
	}
	buf.WriteString("\x1b[")
	buf.WriteString(color_as_sgr(s.bg, false))
	buf.WriteString(";")
	buf.WriteString(color_as_sgr(s.fg, true))
	buf.WriteString("m")
	buf.WriteString(s.text)
	buf.WriteString("\x1b[m")
	return buf.String()
}

func colored_text(text string, fg style.RGBA) string {
	return fmt.Sprintf("\x1b[%sm%s\x1b[39m", color_as_sgr(fg, true), text)
}

func concat_segments_soft(fg style.RGBA, is_right bool, segments ...Segment) (s Segment) {
	segments = utils.Filter(segments, func(s Segment) bool {
		return !s.skip
	})
	if len(segments) == 0 {
		return default_segment("")
	}
	buf := strings.Builder{}
	if is_right {
		s = segments[0]
		for i, x := range segments {
			if i > 0 {
				sep := Segment{text: RIGHT_DIVIDER, fg: fg, bg: x.bg}
				buf.WriteString(sep.styled_text())
			}
			buf.WriteString(x.styled_text())
		}
	} else {
		s = segments[len(segments)-1]
		for i, x := range segments {
			buf.WriteString(x.styled_text())
			if i < len(segments)-1 {
				sep := Segment{text: LEFT_DIVIDER, fg: fg, bg: x.bg}
				buf.WriteString(sep.styled_text())
			}
		}
	}
	s.text = buf.String()
	return
}

func concat_segments_hard(trailing_bg style.RGBA, is_right bool, segments ...Segment) (s Segment) {
	segments = utils.Filter(segments, func(s Segment) bool {
		return !s.skip
	})
	if len(segments) == 0 {
		return default_segment("")
	}
	buf := strings.Builder{}
	if is_right {
		s = segments[0]
		for i, x := range segments {
			sep := Segment{text: RIGHT_END, fg: x.bg, bg: trailing_bg}
			if i > 0 {
				sep.bg = segments[i-1].bg
			}
			buf.WriteString(sep.styled_text())
			buf.WriteString(x.styled_text())
		}
	} else {
		s = segments[len(segments)-1]
		for i, x := range segments {
			buf.WriteString(x.styled_text())
			sep := Segment{text: LEFT_END, fg: x.bg, bg: trailing_bg}
			if i+1 < len(segments) {
				sep.bg = segments[i+1].bg
			}
			buf.WriteString(sep.styled_text())
		}
	}
	s.text = buf.String()
	return
}

func default_segment(text string) Segment {
	return Segment{text: text, bg: DARK_GRAY, fg: WHITE}
}

// system segment {{{
func (self *state) uptime() (s Segment) {
	data, err := os.ReadFile("/proc/uptime")
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("uptime", err)
		}
	}()
	if err != nil {
		return
	}
	fields := strings.Fields(utils.UnsafeBytesToString(data))
	if len(fields) < 1 {
		err = fmt.Errorf("No fields in /proc/uptime")
		return
	}
	var uptime float64
	uptime, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return
	}
	seconds := int(uptime)
	minutes := seconds / 60
	seconds = seconds % 60
	hours := minutes / 60
	minutes = minutes % 60
	days := hours / 24
	hours = hours % 24
	parts := []string{}
	a := func(which int, suffix string) {
		if len(parts) < 2 && which > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", which, suffix))
		}
	}
	a(days, "d")
	a(hours, "h")
	a(minutes, "m")
	a(seconds, "s")
	text := " " + strings.Join(parts, " ") + " "
	return default_segment(text)
}

func (self *state) system_load() (s Segment) {
	if self.num_cpus < 1 {
		self.num_cpus = max(1, runtime.NumCPU())
	}
	data, err := os.ReadFile("/proc/loadavg")
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("system_load", err)
		}
	}()
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		err = fmt.Errorf("insufficient number of fields in /proc/loadavg")
		return
	}
	var a, b, c float64
	if a, err = strconv.ParseFloat(fields[0], 64); err != nil {
		return
	}
	if b, err = strconv.ParseFloat(fields[1], 64); err != nil {
		return
	}
	if c, err = strconv.ParseFloat(fields[2], 64); err != nil {
		return
	}
	f := func(val float64) string {
		normalized := val / float64(self.num_cpus)
		fg := RED
		switch {
		case normalized < 0.5:
			fg = WHITE
		case normalized < 1:
			fg = YELLOW
		}
		return colored_text(fmt.Sprintf("%d", int(normalized*100)), fg)

	}
	return default_segment(fmt.Sprintf(" %s %s %s ", f(a), f(b), f(c)))
}

func (self *state) network_load() (s Segment) {
	if self.network_load_data == nil {
		self.network_load_data = make(map[string]network_load_data)
	}
	data, err := os.ReadFile("/proc/net/route")
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("network_load", err)
		}
	}()
	if err != nil {
		return
	}
	lines := utils.Splitlines(utils.UnsafeBytesToString(data), 16)
	iface := ""
	for _, line := range lines {
		if parts := strings.Split(line, "\t"); len(parts) > 1 {
			if strings.ReplaceAll(parts[1], "0", "") == "" {
				iface = parts[0]
				break
			}
		}
	}
	if iface == "" {
		err = fmt.Errorf("Failed to find default network interface")
		return
	}
	pb := func(which string) (int64, error) {
		if data, e := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, which)); e != nil {
			return 0, e
		} else if rx, e := strconv.ParseInt(strings.TrimSpace(utils.UnsafeBytesToString(data)), 10, 64); e != nil {
			return 0, fmt.Errorf("Failed to parse %s with error: %w", which, e)
		} else {
			return rx, nil
		}

	}
	var rx, tx int64
	if rx, err = pb("rx_bytes"); err != nil {
		return
	}
	if tx, err = pb("tx_bytes"); err != nil {
		return
	}
	n := network_load_data{time: self.now, rx: rx, tx: tx}
	prev, found := self.network_load_data[iface]
	self.network_load_data[iface] = n
	if !found {
		s.skip = true
		return
	}
	dt := n.time.Sub(prev.time).Seconds()
	f := func(cur, prev int64, arrow string, color style.RGBA) string {
		rate := float64(cur-prev) / dt
		prefix := colored_text(arrow, color)
		suffix := `B`
		precision := "%.1f"
		switch {
		case rate >= 1<<30:
			rate /= (1 << 30)
			suffix = "GB"
		case rate >= 1<<20:
			rate /= (1 << 20)
			suffix = "MB"
		case rate >= 1<<10:
			rate /= (1 << 10)
			suffix = "KB"
		default:
			precision = "%.0f"
			rate = math.Round(rate)
		}
		r := colored_text(fmt.Sprintf(precision+suffix, rate), WHITE)
		return fmt.Sprintf("%6s", prefix+r)
	}
	r := f(rx, prev.rx, `⬇`, GREEN)
	t := f(tx, prev.tx, `⬆`, RED)
	return default_segment(" " + r + " " + t + " ")
}

func (self *state) memory_usage() (s Segment) {
	data, err := os.ReadFile("/proc/meminfo")
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("memory_usage", err)
		}
	}()
	if err != nil {
		return
	}
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			available, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if total == 0 {
		return
	}
	used := total - available
	percent := float64(used) / float64(total) * 100
	return default_segment(fmt.Sprintf(" %d%% ", int(percent)))
}

func (self *state) cpu_usage() (s Segment) {
	data, err := os.ReadFile("/proc/stat")
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("cpu_usage", err)
		}
	}()
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}
	var total uint64
	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseUint(fields[i], 10, 64)
		total += val
	}
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	dt := total - self.prev_cpu_total
	di := idle - self.prev_cpu_idle
	self.prev_cpu_total = total
	self.prev_cpu_idle = idle
	if dt == 0 {
		s.skip = true
		return
	}
	percent := 100 * float64(dt-di) / float64(dt)
	return default_segment(fmt.Sprintf(" %d%% ", int(percent)))
}

func (self *state) system() (s Segment) {
	segs := []Segment{
		// self.uptime(),
		self.cpu_usage(),
		self.memory_usage(),
		self.system_load(),
		self.network_load(),
	}
	s = concat_segments_soft(LIGHT_GRAY, true, segs...)
	s.name = "system"
	return s
}

// }}}

// battery {{{
func (self *state) battery() (s Segment) {
	const base = `/sys/class/power_supply`
	if self.battery_history == nil {
		self.battery_history = make(map[string]*battery_history)
	}
	entries, err := os.ReadDir(base)
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("battery", err)
		}
	}()
	r := func(which, x string) (float64, error) {
		data, err := os.ReadFile(base + "/" + which + "/" + x)
		if err != nil {
			return -1, err
		}
		ans, err := strconv.ParseInt(strings.TrimSpace(utils.UnsafeBytesToString(data)), 10, 64)
		if err != nil {
			return -1, err
		}
		return float64(ans) / 1e6, nil
	}
	read_battery_data := func(which string) (bs battery_data, err error) {
		if bs.power_now, err = r(which, "power_now"); err != nil {
			return
		}
		if bs.energy_full, err = r(which, "energy_full"); err != nil {
			return
		}
		if bs.energy_now, err = r(which, "energy_now"); err != nil {
			return
		}
		return
	}
	effective_rate := func(history []float64) (ans float64) {
		for _, x := range history {
			ans += x
		}
		return ans / float64(len(history))
	}
	batteries := []battery_data{}
	for _, which := range entries {
		err = nil
		var bs battery_data
		if bs, err = read_battery_data(which.Name()); err != nil {
			continue
		}
		var d []byte
		if d, err = os.ReadFile(base + "/" + which.Name() + "/status"); err != nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(utils.UnsafeBytesToString(d)))
		if status == "charging" || status == "discharging" {
			bh := self.battery_history[which.Name()]
			if bh == nil {
				bh = &battery_history{}
				self.battery_history[which.Name()] = bh
			}
			bs.is_charging = status == "charging"
			bs.ratio = bs.energy_now / bs.energy_full
			var power float64
			if bs.is_charging {
				bh.charging = append(bh.charging, bs.power_now)
				power = effective_rate(bh.charging)
			} else {
				bh.discharging = append(bh.discharging, bs.power_now)
				power = effective_rate(bh.discharging)
			}
			var t float64
			if power > 0 {
				t = bs.energy_now / power
			}
			bs.hours = int(t)
			bs.minutes = int(60 * (t - float64(bs.hours)))
			batteries = append(batteries, bs)
		}
	}
	err = nil
	if len(batteries) == 0 {
		s.skip = true
		return
	}
	buf := strings.Builder{}
	var first_bg style.RGBA
	for i, bat := range batteries {
		var tleft string
		if bat.hours+bat.minutes > 0 {
			tleft = fmt.Sprintf("%d:%d", bat.hours, bat.minutes)
		} else {
			tleft = fmt.Sprintf("%.1f", bat.ratio*100)
		}
		symbol := utils.IfElse(bat.is_charging, CHARGING, BATTERY)
		col := utils.IfElse(bat.is_charging, GREEN, ORANGE)
		if i == 0 {
			first_bg = col
		}
		buf.WriteString(Segment{fg: BLACK, bg: col, text: " " + symbol + " " + tleft + " "}.styled_text())
	}
	return Segment{text: buf.String(), fg: BLACK, bg: first_bg}
}

// }}}

func (self *state) date() (s Segment) {
	// d := self.now.Format(" Mon 02, Jan ")
	// t := self.now.Format(" " + CLOCK + " 15:04 ")
	t := self.now.Format(" 15:04 ")
	// ds := default_segment(d)
	// ds.bg = MEDIUM_GRAY
	ts := default_segment(t)
	ts.bg = MEDIUM_GRAY
	// return concat_segments_soft(LIGHT_GRAY, true, ds, ts)
	return concat_segments_soft(LIGHT_GRAY, true, ts)
}

// income {{{

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (self *state) income() (s Segment) {
	if !self.income_data.initialized {
		self.income_data.initialized = true
		self.income_data.bg, _ = style.ParseColor(`#333399`)
		self.income_data.fg, _ = style.ParseColor(`#ADD8E6`)

		plans := modelquota.LoadPlans()
		log.Printf("income: starting, %d plans", len(plans))

		go func() {
			for {
				income := modelquota.FetchAll()
				if income == "" {
					log.Println("income: empty (all plans failed)")
				} else {
					self.lock.Lock()
					self.income_data.val = " " + income + " "
					self.lock.Unlock()
					self.lp.WakeupMainThread()
				}
				time.Sleep(time.Second * 60)
			}
		}()
	}
	self.lock.Lock()
	val := self.income_data.val
	self.lock.Unlock()
	s.skip = val == ""
	s.fg = self.income_data.fg
	s.bg = self.income_data.bg
	s.bold = true
	s.text = val
	return
}

// }}}

// mail {{{
func (self *state) mail() (s Segment) {
	var err error
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("mail", err)
		}
	}()
	const socket_name = "\x00mail_scheduler.sock"
	var addr *net.UnixAddr
	if addr, err = net.ResolveUnixAddr("unix", socket_name); err != nil {
		return
	}
	var conn *net.UnixConn
	if conn, err = net.DialUnix("unix", nil, addr); err != nil {
		return
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("simplestatus\x00\x00")); err != nil {
		return
	}
	var data []byte
	if data, err = io.ReadAll(conn); err != nil {
		return
	}
	text := utils.UnsafeBytesToString(bytes.ReplaceAll(data, []byte{0}, []byte{}))
	return Segment{text: " " + text + " ", bold: true, fg: BLACK, bg: utils.IfElse(strings.HasPrefix(text, "0"), WHITE, GREEN)}
}

// }}}

// workspace {{{

func (self *state) set_wm_string(which, value string) {
	self.set_wm_strings(which + ":" + value)
}

func (self *state) set_wm_strings(vals ...string) {
	self.lock.Lock()
	for _, x := range vals {
		which, value, _ := strings.Cut(x, ":")
		switch which {
		case "title":
			self.window_title = value
		case "workspace":
			self.workspace_name = value
		}
	}
	self.lock.Unlock()
	self.lp.WakeupMainThread()
}

func (self *state) workspace() (s Segment) {
	var err error
	defer func() {
		if err != nil {
			s.skip = true
			self.report_failure("workspace", err)
		}
	}()
	if !self.wm_initialized {
		self.wm_initialized = true
		switch {
		case hypr.IsHyprlandRunning():
			if err = hypr.HyprBar(self.set_wm_strings); err != nil {
				return
			}
		case sway.IsSwayRunning():
			if err = sway.SwayBar(self.set_wm_string); err != nil {
				return
			}
		default:
			err = fmt.Errorf("No supported Wayland compositor is running")
		}
	}
	return Segment{text: " " + self.workspace_name + " ", fg: BLACK, bold: true, bg: WHITE}
}

// }}}

func (self *state) update_screen(_ loop.IdType) error {
	self.update_timer = 0
	return self.draw_screen()
}

func (self *state) draw_screen() (err error) {
	sz, _ := self.lp.ScreenSize()
	self.now = time.Now()
	right_text := concat_segments_hard(
		BLACK,
		true,
		self.income(),
		self.system(),
		// self.date(),
		self.battery(),
		// self.mail(),
	).text
	right_sz := wcswidth.Stringwidth(right_text)
	columns := int(sz.WidthCells)
	if columns > right_sz {
		w := self.workspace()
		workspace_sz := wcswidth.Stringwidth(w.text) + 3
		space_for_title := columns - right_sz - workspace_sz
		title := self.window_title
		if space_for_title > 0 {
			ntitle := wcswidth.TruncateToVisualLength(title, space_for_title)
			if len(ntitle) < len(title) {
				ntitle += "…"
			}
			title = " " + ntitle + " "
		} else {
			title = ""
		}
		left_text := concat_segments_hard(
			BLACK,
			false,
			self.date(),
			w,
			Segment{text: title, fg: WHITE, bg: DARK_GRAY},
		).styled_text()
		self.lp.QueueWriteString("\r\x1b[K")
		self.lp.QueueWriteString(left_text)
		rpos := columns - right_sz
		self.lp.MoveCursorTo(rpos+1, 1)
		self.lp.QueueWriteString(right_text)
	}

	if self.update_timer > 0 {
		self.lp.RemoveTimer(self.update_timer)
	}
	self.update_timer, err = self.lp.AddTimer(time.Second, false, self.update_screen)
	return
}

func run_loop() {
	lp, err := loop.New()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	state := state{lp: lp}
	lp.MouseTrackingMode(loop.BUTTONS_ONLY_MOUSE_TRACKING)
	lp.OnInitialize = func() (string, error) {
		lp.AllowLineWrapping(false)
		lp.SetCursorVisible(false)
		return "", state.draw_screen()
	}
	// we can detect clickable widget... but fine for now
	// https://github.com/kovidgoyal/kitty/blob/4d3bbd82e07301bdd6eb1bae5d8c79e8d4fb8d73/tools/tui/loop/mouse.go#L44
	lp.OnMouseEvent = func(ev *loop.MouseEvent) error {
		// Check if MOUSE_WHEEL_UP flag is set in ev.Buttons
		if (ev.Buttons & loop.MOUSE_WHEEL_UP) != 0 {
			go func() { // Run command in a goroutine to prevent blocking the main loop
				cmd := exec.Command("control", "sound", "+")
				if err := cmd.Run(); err != nil {
					// Error is ignored as requested by the user to avoid polluting the bar.
				}
			}()
		} else if (ev.Buttons & loop.MOUSE_WHEEL_DOWN) != 0 { // Check if MOUSE_WHEEL_DOWN flag is set
			go func() { // Run command in a goroutine to prevent blocking the main loop
				cmd := exec.Command("control", "sound", "-")
				if err := cmd.Run(); err != nil {
					// Error is ignored as requested by the user to avoid polluting the bar.
				}
			}()
		}
		return nil
	}
	lp.OnResize = func(old_size loop.ScreenSize, new_size loop.ScreenSize) error {
		return state.draw_screen()
	}
	lp.OnWakeup = func() error {
		return state.draw_screen()
	}
	err = lp.Run()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	os.Exit(lp.ExitCode())
}

func launch_panel() {
	self_exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to get path to self executable: %w", err)
		os.Exit(1)
	}
	kitty := utils.Which("kitty")
	unix.Exec(kitty, []string{
		"kitty", "+kitten", "panel",
		`--override=background=black`,
		`--override=font_size=10`,
		`--edge=bottom`,
		self_exe,
		"bar",
		"inner",
	}, os.Environ())
}

func Main(args []string) {
	if len(args) == 0 {
		launch_panel()
		return
	}
	if args[0] != "inner" {
		fmt.Fprintln(os.Stderr, args[0], "is not a valid argument")
		os.Exit(1)
	}

	// Bar is a kitty TUI process — stdout/stderr both go to the same
	// controlling tty and would interleave with the TUI state machine
	// and corrupt the display. Route log.* through one of:
	//   - syslog/journal (when running under systemd — INVOCATION_ID is
	//     set by systemd per service invocation)
	//   - tty.DebugPrintln (terminal run — TUI-safe, no tty pollution)
	//
	// Detect systemd by env, not by trying syslog.New: on most Linux
	// desktops /dev/log is reachable even when wm bar is launched from
	// a terminal, so a successful syslog connect is not proof of
	// systemd supervision. INVOCATION_ID is the canonical signal.
	//
	// Override with $WM_BAR_LOG=syslog|tty|silent.
	var sink io.Writer
	switch os.Getenv("WM_BAR_LOG") {
	case "silent":
		sink = io.Discard
	case "tty":
		sink = ttyDebugWriter{}
	case "syslog":
		if syslogger, err := syslog.New(syslog.LOG_INFO|syslog.LOG_USER, "wm-bar"); err == nil {
			sink = syslogger
		} else {
			sink = ttyDebugWriter{}
		}
	default:
		if os.Getenv("INVOCATION_ID") != "" {
			if syslogger, err := syslog.New(syslog.LOG_INFO|syslog.LOG_USER, "wm-bar"); err == nil {
				sink = syslogger
			} else {
				sink = ttyDebugWriter{}
			}
		} else {
			sink = ttyDebugWriter{}
		}
	}
	log.SetFlags(log.Lshortfile)
	log.SetOutput(sink)

	DARK_GRAY, _ = style.ParseColor(`#202020`)
	MEDIUM_GRAY, _ = style.ParseColor(`#333333`)
	LIGHT_GRAY, _ = style.ParseColor(`#888888`)
	GREEN, _ = style.ParseColor(`#77dd77`)
	WHITE, _ = style.ParseColor(`white`)
	BLACK, _ = style.ParseColor(`black`)
	YELLOW, _ = style.ParseColor(`yellow`)
	RED, _ = style.ParseColor(`red`)
	ORANGE, _ = style.ParseColor(`orange`)
	DARK_ORANGE, _ = style.ParseColor(`dark orange`)

	run_loop()
}
