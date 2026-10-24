package filter

import (
	"cmp"
	"math/bits"
	"slices"

	"github.com/iomz/tagstrak/v2/internal/identity"
)

// index narrows the subscriptions a subject can match by pattern prefix, then
// confirms every candidate with the reference rule, matches. The structures
// only prune: a pattern is never accepted without matches agreeing, so the
// index can be no more permissive than linear, and it is exact as long as it
// never prunes a true match.
//
// Patterns fall into three groups (see the #7 slice-2 design record):
//
//   - encoding patterns with a bit test go into a per-scheme byte trie over
//     the leading run of masked bits; whatever the mask pins after that run,
//     such as reserved bits, is left to the confirming match
//   - patterns without a bit test whose fields are exact values followed
//     only by stars go into a per-domain trie over component values
//   - everything else, field patterns with ranges, is scanned linearly
//
// A range in a bit-tested pattern, such as a filter value range, ends the
// leading run early, so such patterns sit near the trie root and cost what a
// linear scan of them would.
//
// Lookup cost is the trie depths plus the candidates found plus the residual
// list, independent of how many indexed subscriptions there are.
type index struct {
	ids      []ID // by rank, the subscription's position in Compile's input
	ranks    map[ID]int32
	bitRoots map[identity.SchemeID]*bitNode
	idRoots  map[domain]*fieldNode
	encRoots map[identity.SchemeID]*fieldNode
	residual []entry
}

type entry struct {
	rank    int32
	pattern identity.Pattern
}

// bitNode is one byte of masked prefix. Entries ending inside the next byte
// carry the remaining high bits to test before confirming.
type bitNode struct {
	keys     []byte // sorted
	children []*bitNode
	entries  []bitEntry
}

type bitEntry struct {
	entry
	remMask, remValue byte // over the next byte; zero mask means none
}

// fieldNode is one exact component value. Entries at a node have that many
// exact fields followed only by stars.
type fieldNode struct {
	children map[string]*fieldNode
	entries  []entry
}

// domain keys identity-level patterns, which have no scheme.
type domain struct {
	authority identity.Authority
	keyType   identity.KeyType
}

func compileIndex(subscriptions []Subscription) (*index, error) {
	if err := validate(subscriptions); err != nil {
		return nil, err
	}
	m := &index{
		ids:      make([]ID, len(subscriptions)),
		ranks:    make(map[ID]int32, len(subscriptions)),
		bitRoots: map[identity.SchemeID]*bitNode{},
		idRoots:  map[domain]*fieldNode{},
		encRoots: map[identity.SchemeID]*fieldNode{},
	}
	for rank, s := range subscriptions {
		m.ids[rank] = s.ID
		m.ranks[s.ID] = int32(rank)
		for _, p := range s.Patterns {
			m.add(entry{rank: int32(rank), pattern: p})
		}
	}
	return m, nil
}

func (m *index) add(e entry) {
	p := e.pattern
	if p.Level() == identity.LevelEncoding && p.Bits() != nil {
		n := leadingOnes(p.Bits().Mask())
		value := p.Bits().Value()
		node := m.bitRoots[p.Scheme()]
		if node == nil {
			node = &bitNode{}
			m.bitRoots[p.Scheme()] = node
		}
		full := n / 8
		for _, b := range value[:full] {
			node = node.child(b)
		}
		be := bitEntry{entry: e}
		if rem := n % 8; rem != 0 {
			be.remMask = byte(0xff << (8 - rem))
			be.remValue = value[full] & be.remMask
		}
		node.entries = append(node.entries, be)
		return
	}
	exact, ok := exactPrefix(p)
	if !ok {
		m.residual = append(m.residual, e)
		return
	}
	var node *fieldNode
	switch p.Level() {
	case identity.LevelIdentity:
		key := domain{authority: p.Authority(), keyType: p.KeyType()}
		if node = m.idRoots[key]; node == nil {
			node = &fieldNode{}
			m.idRoots[key] = node
		}
	default:
		if node = m.encRoots[p.Scheme()]; node == nil {
			node = &fieldNode{}
			m.encRoots[p.Scheme()] = node
		}
	}
	for i := range exact {
		text := p.Field(i).Text
		next := node.children[text]
		if next == nil {
			if node.children == nil {
				node.children = map[string]*fieldNode{}
			}
			next = &fieldNode{}
			node.children[text] = next
		}
		node = next
	}
	node.entries = append(node.entries, e)
}

func (n *bitNode) child(b byte) *bitNode {
	i, found := slices.BinarySearch(n.keys, b)
	if !found {
		n.keys = slices.Insert(n.keys, i, b)
		n.children = slices.Insert(n.children, i, &bitNode{})
	}
	return n.children[i]
}

// leadingOnes counts the mask's leading one bits.
func leadingOnes(mask []byte) int {
	n := 0
	for _, b := range mask {
		if b != 0xff {
			return n + bits.LeadingZeros8(^b)
		}
		n += 8
	}
	return n
}

// exactPrefix reports how many leading exact fields a pattern has, if the
// rest are all stars.
func exactPrefix(p identity.Pattern) (int, bool) {
	exact := 0
	for exact < p.Len() && p.Field(exact).Kind == identity.FieldExact {
		exact++
	}
	for i := exact; i < p.Len(); i++ {
		if p.Field(i).Kind != identity.FieldAny {
			return 0, false
		}
	}
	return exact, true
}

func (m *index) Match(s Subject, dst []ID) []ID {
	start := len(dst)
	if node := m.bitRoots[s.Scheme]; node != nil {
		dst = m.walkBits(node, s, dst)
	}
	if node := m.encRoots[s.Scheme]; node != nil {
		dst = m.walkFields(node, s.Fields, nil, s, dst)
	}
	if node := m.idRoots[domain{authority: s.Identity.Authority(), keyType: s.Identity.KeyType()}]; node != nil {
		dst = m.walkFields(node, nil, &s.Identity, s, dst)
	}
	for _, e := range m.residual {
		if matches(e.pattern, s) {
			dst = append(dst, m.ids[e.rank])
		}
	}
	return m.order(dst, start)
}

func (m *index) walkBits(node *bitNode, s Subject, dst []ID) []ID {
	for depth := 0; ; depth++ {
		var next byte
		haveNext := depth < len(s.Bits)
		if haveNext {
			next = s.Bits[depth]
		}
		for _, e := range node.entries {
			if e.remMask != 0 && (!haveNext || next&e.remMask != e.remValue) {
				continue
			}
			if matches(e.pattern, s) {
				dst = append(dst, m.ids[e.rank])
			}
		}
		if !haveNext {
			return dst
		}
		i, found := slices.BinarySearch(node.keys, next)
		if !found {
			return dst
		}
		node = node.children[i]
	}
}

// walkFields follows exact component values from the root. Exactly one of
// fields and value supplies the components.
func (m *index) walkFields(node *fieldNode, fields []identity.Component, value *identity.Value, s Subject, dst []ID) []ID {
	n := len(fields)
	if value != nil {
		n = value.Len()
	}
	for depth := 0; ; depth++ {
		for _, e := range node.entries {
			if matches(e.pattern, s) {
				dst = append(dst, m.ids[e.rank])
			}
		}
		if depth == n || node.children == nil {
			return dst
		}
		var c identity.Component
		if value != nil {
			c = value.At(depth)
		} else {
			c = fields[depth]
		}
		next := node.children[c.Value]
		if next == nil {
			return dst
		}
		node = next
	}
}

// order restores the contract's subscription order and removes duplicates
// from a subscription matching through several patterns, in place.
func (m *index) order(dst []ID, start int) []ID {
	found := dst[start:]
	if len(found) < 2 {
		return dst
	}
	byRank := func(a, b ID) int { return cmp.Compare(m.ranks[a], m.ranks[b]) }
	if !slices.IsSortedFunc(found, byRank) {
		slices.SortFunc(found, byRank)
	}
	return append(dst[:start], slices.Compact(found)...)
}

func (m *index) Len() int { return len(m.ids) }
