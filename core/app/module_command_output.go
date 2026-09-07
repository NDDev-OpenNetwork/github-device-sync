package app

import "sync"

// Keep a bounded tail while the process runs, rather than accumulating an
// arbitrary build log and truncating only after the command has exited.
type moduleCommandOutput struct {
	mu   sync.Mutex
	tail []byte
}

const moduleCommandOutputLimit = 16 << 10

func (output *moduleCommandOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	written := len(value)
	if len(value) >= moduleCommandOutputLimit {
		output.tail = append(output.tail[:0], value[len(value)-moduleCommandOutputLimit:]...)
		return written, nil
	}
	if discard := len(output.tail) + len(value) - moduleCommandOutputLimit; discard > 0 {
		copy(output.tail, output.tail[discard:])
		output.tail = output.tail[:len(output.tail)-discard]
	}
	output.tail = append(output.tail, value...)
	return written, nil
}

func (output *moduleCommandOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return string(output.tail)
}
