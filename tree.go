package treemux

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type handlerMap struct {
	get     HandlerFunc
	post    HandlerFunc
	put     HandlerFunc
	delete  HandlerFunc
	head    HandlerFunc
	options HandlerFunc
	patch   HandlerFunc

	// If true, the head handler was set implicitly, so let it also be set explicitly.
	implicitHead bool

	m map[string]HandlerFunc
}

func newHandlerMap() *handlerMap {
	return new(handlerMap)
}

func (h *handlerMap) Map() map[string]HandlerFunc {
	return h.m
}

func (h *handlerMap) Get(name string) HandlerFunc {
	switch name {
	case http.MethodGet:
		return h.get
	case http.MethodPost:
		return h.post
	case http.MethodPut:
		return h.put
	case http.MethodDelete:
		return h.delete
	case http.MethodHead:
		return h.head
	case http.MethodOptions:
		return h.options
	case http.MethodPatch:
		return h.patch
	default:
		return h.m[name]
	}
}

func (h *handlerMap) Set(name string, handler HandlerFunc) {
	switch name {
	case http.MethodGet:
		h.get = handler
	case http.MethodPost:
		h.post = handler
	case http.MethodPut:
		h.put = handler
	case http.MethodDelete:
		h.delete = handler
	case http.MethodHead:
		h.head = handler
	case http.MethodOptions:
		h.options = handler
	case http.MethodPatch:
		h.patch = handler
	}

	if h.m == nil {
		h.m = make(map[string]HandlerFunc)
	}
	h.m[name] = handler
}

type node struct {
	route string
	path  string

	priority int

	// The list of static children to check.
	staticIndices []byte
	staticChild   []*node

	// If none of the above match, check the wildcard children
	wildcardChild *node

	// If none of the above match, then we use the catch-all, if applicable.
	catchAllChild *node

	// Data for the node is below.

	addSlash   bool
	isCatchAll bool

	// If this node is the end of the URL, then call the handler, if applicable.
	handlerMap *handlerMap

	// The names of the parameters to apply.
	leafWildcardNames []string
}

func (n *node) paramName(i int) string {
	return n.leafWildcardNames[len(n.leafWildcardNames)-1-i]
}

func (n *node) sortStaticChild(i int) {
	for i > 0 && n.staticChild[i].priority > n.staticChild[i-1].priority {
		n.staticChild[i], n.staticChild[i-1] = n.staticChild[i-1], n.staticChild[i]
		n.staticIndices[i], n.staticIndices[i-1] = n.staticIndices[i-1], n.staticIndices[i]
		i -= 1
	}
}

func (n *node) setHandler(verb string, handler HandlerFunc, implicitHead bool) {
	if n.handlerMap == nil {
		n.handlerMap = newHandlerMap()
	}
	if h := n.handlerMap.Get(verb); h != nil &&
		(verb != http.MethodHead || !n.handlerMap.implicitHead) {
		panic(fmt.Sprintf("%s already handles %s", n.path, verb))
	}
	n.handlerMap.Set(verb, handler)
	if verb == http.MethodHead {
		n.handlerMap.implicitHead = implicitHead
	}
}

func (n *node) addPath(path string, wildcards []string, inStaticToken bool) *node {
	leaf := len(path) == 0
	if leaf {
		if wildcards != nil {
			// Make sure the current wildcards are the same as the old ones.
			// If not then we have an ambiguous path.
			if n.leafWildcardNames != nil {
				if len(n.leafWildcardNames) != len(wildcards) {
					// This should never happen.
					panic("Reached leaf node with differing wildcard array length. Please report this as a bug.")
				}

				for i := 0; i < len(wildcards); i++ {
					if n.leafWildcardNames[i] != wildcards[i] {
						panic(fmt.Sprintf("Wildcards %v are ambiguous with wildcards %v",
							n.leafWildcardNames, wildcards))
					}
				}
			} else {
				// No wildcards yet, so just add the existing set.
				n.leafWildcardNames = wildcards
			}
		}

		return n
	}

	c := path[0]
	nextSlash := strings.IndexByte(path, '/')
	var thisToken string
	var tokenEnd int

	if c == '/' {
		// Done processing the previous token, so reset inStaticToken to false.
		thisToken = "/"
		tokenEnd = 1
	} else if nextSlash == -1 {
		thisToken = path
		tokenEnd = len(path)
	} else {
		thisToken = path[0:nextSlash]
		tokenEnd = nextSlash
	}
	remainingPath := path[tokenEnd:]

	if c == '*' && !inStaticToken {
		// Token starts with a *, so it's a catch-all
		thisToken = thisToken[1:]
		if n.catchAllChild == nil {
			n.catchAllChild = &node{path: thisToken, isCatchAll: true}
		}

		if path[1:] != n.catchAllChild.path {
			panic(fmt.Sprintf("Catch-all name in %s doesn't match %s. You probably tried to define overlapping catchalls",
				path, n.catchAllChild.path))
		}

		if nextSlash != -1 {
			panic("/ after catch-all found in " + path)
		}

		if wildcards == nil {
			wildcards = []string{thisToken}
		} else {
			wildcards = append(wildcards, thisToken)
		}
		n.catchAllChild.leafWildcardNames = wildcards

		return n.catchAllChild
	}

	if c == ':' && !inStaticToken {
		// Token starts with a :
		thisToken = thisToken[1:]

		if wildcards == nil {
			wildcards = []string{thisToken}
		} else {
			wildcards = append(wildcards, thisToken)
		}

		if n.wildcardChild == nil {
			n.wildcardChild = &node{path: "wildcard"}
		}

		return n.wildcardChild.addPath(remainingPath, wildcards, false)
	}

	// if strings.ContainsAny(thisToken, ":*") {
	// 	panic("* or : in middle of path component " + path)
	// }

	var unescaped bool

	if len(thisToken) >= 2 && !inStaticToken {
		if thisToken[0] == '\\' && (thisToken[1] == '*' || thisToken[1] == ':' || thisToken[1] == '\\') {
			// The token starts with a character escaped by a backslash. Drop the backslash.
			c = thisToken[1]
			thisToken = thisToken[1:]
			unescaped = true
		}
	}

	// Set inStaticToken to ensure that the rest of this token is not mistaken
	// for a wildcard if a prefix split occurs at a '*' or ':'.
	inStaticToken = (c != '/')

	// Do we have an existing node that starts with the same letter?
	for i, index := range n.staticIndices {
		if c == index {
			// Yes. Split it based on the common prefix of the existing
			// node and the new one.
			child, prefixSplit := n.splitCommonPrefix(i, thisToken)

			child.priority++
			n.sortStaticChild(i)
			if unescaped {
				// Account for the removed backslash.
				prefixSplit++
			}
			return child.addPath(path[prefixSplit:], wildcards, inStaticToken)
		}
	}

	// No existing node starting with this letter, so create it.
	child := &node{path: thisToken}

	if n.staticIndices == nil {
		n.staticIndices = []byte{c}
		n.staticChild = []*node{child}
	} else {
		n.staticIndices = append(n.staticIndices, c)
		n.staticChild = append(n.staticChild, child)
	}
	return child.addPath(remainingPath, wildcards, inStaticToken)
}

func (n *node) splitCommonPrefix(existingNodeIndex int, path string) (*node, int) {
	childNode := n.staticChild[existingNodeIndex]

	if strings.HasPrefix(path, childNode.path) {
		// No split needs to be done. Rather, the new path shares the entire
		// prefix with the existing node, so the new node is just a child of
		// the existing one. Or the new path is the same as the existing path,
		// which means that we just move on to the next token. Either way,
		// this return accomplishes that
		return childNode, len(childNode.path)
	}

	var i int
	// Find the length of the common prefix of the child node and the new path.
	for i = range childNode.path {
		if i == len(path) {
			break
		}
		if path[i] != childNode.path[i] {
			break
		}
	}

	commonPrefix := path[0:i]
	childNode.path = childNode.path[i:]

	// Create a new intermediary node in the place of the existing node, with
	// the existing node as a child.
	newNode := &node{
		path:     commonPrefix,
		priority: childNode.priority,
		// Index is the first letter of the non-common part of the path.
		staticIndices: []byte{childNode.path[0]},
		staticChild:   []*node{childNode},
	}
	n.staticChild[existingNodeIndex] = newNode

	return newNode, i
}

func (n *node) search(method, path string) (found *node, handler HandlerFunc, params []Param) {
	// if test != nil {
	// 	test.Logf("Searching for %s in %s", path, n.dumpTree("", ""))
	// }
	pathLen := len(path)
	if pathLen == 0 {
		if n.handlerMap == nil {
			return nil, nil, nil
		}
		return n, n.handlerMap.Get(method), nil
	}

	// First see if this matches a static token.
	firstChar := path[0]
	for i, staticIndex := range n.staticIndices {
		if staticIndex == firstChar {
			child := n.staticChild[i]
			childPathLen := len(child.path)
			if pathLen >= childPathLen && child.path == path[:childPathLen] {
				nextPath := path[childPathLen:]
				found, handler, params = child.search(method, nextPath)
			}
			break
		}
	}

	// If we found a node and it had a valid handler, then return here. Otherwise
	// let's remember that we found this one, but look for a better match.
	if handler != nil {
		return
	}

	if n.wildcardChild != nil {
		// Didn't find a static token, so check for a wildcard.
		nextSlash := strings.IndexByte(path, '/')
		if nextSlash < 0 {
			nextSlash = pathLen
		}

		thisToken := path[:nextSlash]
		nextToken := path[nextSlash:]

		if len(thisToken) > 0 { // Don't match on empty tokens.
			wcNode, wcHandler, wcParams := n.wildcardChild.search(method, nextToken)
			if wcHandler != nil || (found == nil && wcNode != nil) {
				unescaped, err := url.PathUnescape(thisToken)
				if err != nil {
					unescaped = thisToken
				}

				if wcParams == nil {
					wcParams = []Param{{
						Name:  wcNode.paramName(0),
						Value: unescaped,
					}}
				} else {
					wcParams = append(wcParams, Param{
						Name:  wcNode.paramName(len(wcParams)),
						Value: unescaped,
					})
				}

				if wcHandler != nil {
					return wcNode, wcHandler, wcParams
				}

				// Didn't actually find a handler here, so remember that we
				// found a node but also see if we can fall through to the
				// catchall.
				found = wcNode
				handler = wcHandler
				params = wcParams
			}
		}
	}

	catchAllChild := n.catchAllChild
	if catchAllChild != nil {
		// Hit the catchall, so just assign the whole remaining path if it
		// has a matching handler.
		handler = catchAllChild.handlerMap.Get(method)
		// Found a handler, or we found a catchall node without a handler.
		// Either way, return it since there's nothing left to check after this.
		if handler != nil || found == nil {
			unescaped, err := url.PathUnescape(path)
			if err != nil {
				unescaped = path
			}

			return catchAllChild, handler, []Param{{
				Name:  catchAllChild.paramName(0),
				Value: unescaped,
			}}
		}

	}

	return found, handler, params
}

func (n *node) dumpTree(prefix, nodeType string) string {
	line := fmt.Sprintf("%s %02d %s%s [%d] %v wildcards %v\n", prefix, n.priority, nodeType, n.path,
		len(n.staticChild), n.handlerMap, n.leafWildcardNames)
	prefix += "  "
	for _, node := range n.staticChild {
		line += node.dumpTree(prefix, "")
	}
	if n.wildcardChild != nil {
		line += n.wildcardChild.dumpTree(prefix, ":")
	}
	if n.catchAllChild != nil {
		line += n.catchAllChild.dumpTree(prefix, "*")
	}
	return line
}
