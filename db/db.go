package db

type Database interface {
	// basic db management operations
	Close()
	Reset() error
	String() string
	Import([]byte) error
	// user methods
	UserByEmail(string) (*User, error)
	SetUser(*User) error
	DelUser(*User) error
}
