// This file declares the records, requests and results exchanged with
// herdr-core, with the JSON names its API uses.

package hc

import "encoding/json"

type (
	// Hello is the core's answer to the handshake.
	Hello struct {
		// Generation is the protocol generation the core speaks.
		Generation uint32 `json:"generation"`
		// Version is the core's release version, such as "0.1.22".
		Version string `json:"version"`
		// Features lists optional protocol features the core supports.
		Features []string `json:"features"`
		// Framed confirms the connection switched to framed mode.
		Framed bool `json:"framed"`
	}

	// CoreInfo identifies one core.
	CoreInfo struct {
		// ID is the core's permanent random id.
		ID string `json:"id"`
		// Name is its display name, the hostname by default.
		Name string `json:"name"`
	}

	// ServerStatus describes the running core.
	ServerStatus struct {
		// Core identifies the core.
		Core CoreInfo `json:"core"`
		// Version is the core's release version.
		Version string `json:"version"`
		// PID is the core's process id.
		PID uint32 `json:"pid"`
		// UptimeMS is how long the core has run, in milliseconds.
		UptimeMS uint64 `json:"uptime_ms"`
		// Live counts terminals whose state is "live".
		Live uint64 `json:"live"`
		// Finished counts terminals whose state is "finished".
		Finished uint64 `json:"finished"`
	}

	// Terminal is the core's record of one terminal. Fields the core reports
	// as null decode to their zero value unless zero is itself meaningful, in
	// which case the field is a pointer.
	Terminal struct {
		// Address names the terminal on its core forever, such as "t7".
		Address string `json:"address"`
		// Name is its optional unique name among live terminals.
		Name string `json:"name"`
		// Parent is the address of the terminal that started it, if any.
		Parent string `json:"parent"`
		// Labels are its free-form key=value labels.
		Labels map[string]string `json:"labels"`
		// Argv is the program it was started with.
		Argv []string `json:"argv"`
		// Cwd is the folder it was started in.
		Cwd string `json:"cwd"`
		// State is StateLive or StateFinished.
		State string `json:"state"`
		// PID is the program's process id; zero once it has ended.
		PID uint32 `json:"pid"`
		// CreatedMS is when it was created, in milliseconds since the epoch.
		CreatedMS uint64 `json:"created_ms"`
		// FinishedMS is when its program ended; zero while it is live.
		FinishedMS uint64 `json:"finished_ms"`
		// Reason says why its program ended or is pending ("exited",
		// "killed", "server_stop", ...); empty while it runs normally.
		Reason string `json:"reason"`
		// ExitCode is the program's exit code; nil while it runs or when a
		// signal ended it.
		ExitCode *int32 `json:"exit_code"`
		// Signal is the signal that ended the program; nil otherwise.
		Signal *int32 `json:"signal"`
		// Cols and Rows are the terminal's grid size in cells.
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
		// Title is the window title the program set.
		Title string `json:"title"`
		// Turn goes up each time the reported status leaves "working".
		Turn uint64 `json:"turn"`
		// Status is what the program last reported about itself; nil when it
		// never reported or after it ended.
		Status *Status `json:"status"`
		// StatusMS is when Status last changed; zero when it never reported.
		StatusMS uint64 `json:"status_ms"`
		// Records holds the program's other status reports by id.
		Records map[string]Status `json:"records"`
		// Reported says whether the program ever reported a status.
		Reported bool `json:"reported"`
		// Foreground is the process in the terminal's foreground now; nil when
		// the core reports none or the terminal has finished.
		Foreground *Foreground `json:"foreground"`
	}

	// Status is one status report a program made about itself (OSC 7501).
	// The words are the program's own.
	Status struct {
		// State is "idle", "working", "done", "blocked" or "error".
		State string `json:"state"`
		// Kind says what a blocked program waits for: "permission",
		// "question" or "auth". It is empty when the program did not say.
		Kind string `json:"kind"`
		// App is the reporting program's name for itself.
		App string `json:"app"`
		// Title and Msg are the program's short and long descriptions.
		Title string `json:"title"`
		Msg   string `json:"msg"`
		// Progress is 0..100 when the program reported one.
		Progress *uint8 `json:"progress"`
	}

	// Foreground is the leader of a terminal's foreground process group.
	Foreground struct {
		// PID is the leader's process id.
		PID int32 `json:"pid"`
		// Name is the process name as the operating system has it.
		Name string `json:"name"`
		// Argv is its command line.
		Argv []string `json:"argv"`
		// Cwd is its working folder now.
		Cwd string `json:"cwd"`
	}

	// Size is a terminal grid size in cells.
	Size struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}

	// Display tells the core what a viewer's own terminal looks like, so the
	// core can answer a program's color and cell-size queries.
	Display struct {
		// FG, BG and Cursor are colors as "#rrggbb".
		FG     string `json:"fg,omitempty"`
		BG     string `json:"bg,omitempty"`
		Cursor string `json:"cursor,omitempty"`
		// Cell is the cell size in pixels, width then height.
		Cell *[2]uint32 `json:"cell,omitempty"`
	}

	// RunRequest starts a program in a new terminal.
	RunRequest struct {
		// Argv is the program and its arguments. Empty starts the core
		// machine's own shell.
		Argv []string `json:"argv,omitempty"`
		// Cwd is the working folder. Empty uses the core's own.
		Cwd string `json:"cwd,omitempty"`
		// Env overrides variables in the core's environment.
		Env map[string]string `json:"env,omitempty"`
		// Labels are the terminal's initial labels.
		Labels map[string]string `json:"labels,omitempty"`
		// Name is an optional name, unique among live terminals: 1 to 32
		// characters of a-z, 0-9, "-", "_" and ".".
		Name string `json:"name,omitempty"`
		// NoParent starts it without a parent terminal.
		NoParent bool `json:"no_parent,omitempty"`
		// OutliveParent keeps it running when its parent's program ends.
		OutliveParent bool `json:"outlive_parent,omitempty"`
		// NoNotify stops the core typing notes about it into its parent.
		NoNotify bool `json:"no_notify,omitempty"`
		// Size is the grid size before any viewer attaches. Nil uses the
		// core's configured size.
		Size *Size `json:"size,omitempty"`
	}

	// ListRequest selects the terminals List returns.
	ListRequest struct {
		// All includes finished terminals.
		All bool `json:"all,omitempty"`
		// Labels keeps only terminals carrying every one of these labels.
		Labels map[string]string `json:"labels,omitempty"`
	}

	// SendRequest pastes text into a terminal and presses Enter.
	SendRequest struct {
		// Terminal is the terminal's address or name.
		Terminal string `json:"terminal"`
		// Text is pasted, then submitted with Enter.
		Text string `json:"text"`
		// If, when set, makes the core type only if the terminal's status is
		// exactly this word. The core accepts "idle" and "working".
		If string `json:"if,omitempty"`
		// Force types even when the program is blocked on a dialog.
		Force bool `json:"force,omitempty"`
	}

	// SendResult reports what the core observed when the text went in.
	SendResult struct {
		// Turn is the terminal's turn counter when the text went in.
		Turn uint64 `json:"turn"`
		// Status is the program's status word then; empty when it never
		// reported one.
		Status string `json:"status"`
		// Started says whether the program went "working" within a few
		// seconds; nil when it does not report status.
		Started *bool `json:"started"`
	}

	// KeysResult reports the terminal's turn and status after keys were
	// pressed.
	KeysResult struct {
		// Turn is the terminal's turn counter.
		Turn uint64 `json:"turn"`
		// Status is the program's status word; empty when it never reported.
		Status string `json:"status"`
	}

	// InterruptResult reports what pressing the stop key did.
	InterruptResult struct {
		// Outcome is "stopped", "exited", "still_working" or "unknown".
		Outcome string `json:"outcome"`
		// Key is the key the core pressed: "esc" or "ctrl+c".
		Key string `json:"key"`
	}

	// AttachRequest opens a live stream to a terminal.
	AttachRequest struct {
		// Terminal is the terminal's address or name.
		Terminal string
		// Cols and Rows are the viewer's size in cells.
		Cols, Rows uint16
		// History is how many scrollback lines the first paint includes.
		History uint32
		// Observe attaches a viewer that never types or resizes. Only an
		// observing viewer receives TerminalResized.
		Observe bool
		// Display describes the viewer's own colors and cell size. Nil lets
		// the core use its defaults.
		Display *Display
	}

	// CloseReason says how a terminal's program ended. The zero value means
	// the stream closed without the program ending, as after a detach.
	CloseReason struct {
		// Reason is "exited", "killed", "server_stop" and so on.
		Reason string `json:"reason"`
		// ExitCode is the program's exit code, when it exited with one.
		ExitCode *int32 `json:"exit_code"`
		// Signal is the signal that ended it, when one did.
		Signal *int32 `json:"signal"`
		// FinishedMS is when it ended; zero when the terminal stays live
		// waiting for a new shell.
		FinishedMS uint64 `json:"finished_ms"`
	}
)

type (
	// rpcRequest is a JSON-RPC 2.0 request or, without an ID, a notification.
	rpcRequest struct {
		JSONRPC string  `json:"jsonrpc"`
		ID      *uint64 `json:"id,omitempty"`
		Method  string  `json:"method"`
		Params  any     `json:"params,omitempty"`
	}

	// rpcMessage is any JSON-RPC 2.0 message the core sends: a reply carries
	// ID with Result or Error; a notification carries Method and Params.
	rpcMessage struct {
		ID     *uint64         `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}

	// rpcError is the core's JSON-RPC error object.
	rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Kind string `json:"kind"`
			Hint string `json:"hint"`
		} `json:"data"`
	}

	// helloParams asks for framed mode and names the features this client
	// understands.
	helloParams struct {
		Framed     bool     `json:"framed"`
		Generation uint32   `json:"generation"`
		Features   []string `json:"features"`
	}

	// terminalParams names the one terminal a call acts on.
	terminalParams struct {
		Terminal string `json:"terminal"`
	}

	// labelParams sets labels on a terminal; an empty value removes a label.
	labelParams struct {
		Terminal string            `json:"terminal"`
		Labels   map[string]string `json:"labels"`
	}

	// keysParams presses named keys in a terminal.
	keysParams struct {
		Terminal string   `json:"terminal"`
		Keys     []string `json:"keys"`
	}

	// attachParams is the terminal.attach request. ResizeNotifications is
	// always asked for; the core confirms it only for an observer.
	attachParams struct {
		Terminal            string   `json:"terminal"`
		Cols                uint16   `json:"cols"`
		Rows                uint16   `json:"rows"`
		History             uint32   `json:"history"`
		Observe             bool     `json:"observe"`
		ResizeNotifications bool     `json:"resize_notifications"`
		Display             *Display `json:"display,omitempty"`
	}

	// attachResult is the terminal.attach reply.
	attachResult struct {
		Channel             uint32   `json:"channel"`
		Terminal            Terminal `json:"terminal"`
		ResizeNotifications bool     `json:"resize_notifications"`
		AutomaticInput      bool     `json:"automatic_input"`
	}

	// subscribeParams is the events.subscribe request.
	subscribeParams struct {
		Labels map[string]string `json:"labels,omitempty"`
	}

	// subscribeResult is the events.subscribe reply. Channel is nil only on a
	// plain connection, which this client never uses.
	subscribeResult struct {
		Seq       uint64     `json:"seq"`
		Terminals []Terminal `json:"terminals"`
		Channel   *uint32    `json:"channel"`
	}

	// listResult is the terminal.list reply.
	listResult struct {
		Terminals []Terminal `json:"terminals"`
	}
)

// Terminal states, as Terminal.State reports them.
const (
	// StateLive means the terminal exists and may have a program running.
	StateLive = "live"
	// StateFinished means the terminal's program ended; the record stays
	// until it is removed.
	StateFinished = "finished"
)

// kindError converts the core's JSON-RPC error object.
func (e *rpcError) kindError() *KindError {
	kind := e.Data.Kind
	if kind == "" {
		kind = "internal"
	}
	return &KindError{Code: e.Code, Kind: kind, Message: e.Message, Hint: e.Data.Hint}
}
