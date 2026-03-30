package dto

type JwtClaims struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

type RemoteUpload struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Action   string `json:"action"` // "download" or "cancel"
}

type Node struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // "file" or "dir"
	Size     int64   `json:"size,omitempty"`
	Children []*Node `json:"children,omitempty"`
}
