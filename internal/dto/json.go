package dto

type JwtClaims struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

type Node struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // "file" or "dir"
	Size     int64   `json:"size,omitempty"`
	Children []*Node `json:"children,omitempty"`
}
