package store

type Kind int

const (
	File Kind = iota
	Dir
)

type Metadata struct {
	Kind                                     Kind
	Ephemeral                                bool
	InstanceNum, ContentGen, LockGen, ACLGen int64
	Checksum                                 uint64
}

type Handle struct {
	Checksum uint64
	SequenceNum int64
	modeInfo
}
func New() *Store
func (s *Store) Create (path string, kind Kind, ephemeral bool) (Metadata, error)
func (s *Store) Stat (path string) (Metadata, error)
func (s *Store) GetContents (path string) ([]byte, Metadata, error)
func (s *Store) SetContents (path string, data []byte....) (Metadata, error)
func (s *Store) ReadDir (path string) ([]string, error)
func (s *Store) Delete (path string) (error)


var ( ErrNotFound, ErrExists, ErrNotDir, ErrIsDir, ErrNotEmpty, ErrGenMismatch error)