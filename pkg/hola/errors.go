package hola

import "errors"

var (
	// ErrDuplicateNode is returned when a node ID is added twice.
	ErrDuplicateNode = errors.New("duplicate node")
	// ErrDuplicateEdge is returned when an edge between the same pair of
	// nodes is added twice.
	ErrDuplicateEdge = errors.New("duplicate edge")
	// ErrUnknownNode is returned when an edge references a missing node.
	ErrUnknownNode = errors.New("unknown node")
	// ErrSelfLoop is returned when an edge connects a node to itself.
	ErrSelfLoop = errors.New("self loop")
	// ErrInvalidSize is returned when a node has a non-positive size.
	ErrInvalidSize = errors.New("invalid node size")
)
