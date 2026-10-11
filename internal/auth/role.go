package auth

// Role mirrors the member_role enum.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Allows reports whether r meets or exceeds min.
func (r Role) Allows(min Role) bool {
	return r.rank() > 0 && r.rank() >= min.rank()
}
