package filelock

import (
	"os"
	"sync"
)

type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

func Acquire(filename string) (*Lock, error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := acquire(file); err != nil {
		file.Close()
		return nil, err
	}
	return &Lock{file: file}, nil
}
func (l *Lock) Close() error { l.once.Do(func() { l.err = l.file.Close() }); return l.err }
