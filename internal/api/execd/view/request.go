package view

type CommandRequest struct {
	Command    string            `json:"command"`
	Stdin      string            `json:"stdin,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Background bool              `json:"background,omitempty"`
	UID        *int              `json:"uid,omitempty"`
	GID        *int              `json:"gid,omitempty"`
	TimeoutMs  int64             `json:"timeout,omitempty"`
	Envs       map[string]string `json:"envs,omitempty"`
}

type SessionCreateRequest struct {
	Cwd string `json:"cwd"`
}
type SessionRunRequest struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout int64  `json:"timeout"`
}
type SessionCreated struct {
	ID string `json:"session_id"`
}

type MoveItem struct {
	Src  string `json:"src"`
	Dest string `json:"dest"`
}
type ReplaceItem struct {
	Old string `json:"old"`
	New string `json:"new"`
}
type ReplaceResult struct {
	ReplacedCount int `json:"replacedCount"`
}
