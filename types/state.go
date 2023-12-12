package types

import (
	"sync"
)

// StateMap wraps sync.Map with type safety
// maps source tx hash -> MessageState
type StateMap struct {
	internal sync.Map
}

func NewStateMap() *StateMap {
	return &StateMap{
		internal: sync.Map{},
	}
}

// load loads the message states tied to a specific transaction hash
func (sm *StateMap) Load(key string) (value []*MessageState, ok bool) {
	internalResult, ok := sm.internal.Load(key)
	if !ok {
		return nil, ok
	}
	return internalResult.([]*MessageState), ok
}

func (sm *StateMap) Delete(key string) {
	sm.internal.Delete(key)
}

// store stores the message states tied to a specific transaction hash
func (sm *StateMap) Store(key string, value []*MessageState) {
	sm.internal.Store(key, value)
}
