package tile57gate

import "sync"

// encControl serializes heavy/raw ENC control-plane operations.
//
// It must NOT be used by runtime tile serving.
//
// Current tile57 native I/O can abort when a raw ENC metadata scan
// overlaps with a BakeTree operation. Keeping these operations
// mutually exclusive also avoids unnecessary disk and memory
// contention while a multi-worker bake is running.
var encControl sync.Mutex

func Lock() {
	encControl.Lock()
}

func Unlock() {
	encControl.Unlock()
}
