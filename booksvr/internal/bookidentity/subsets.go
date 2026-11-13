package bookidentity

// CompatibleSubset excludes unrelated conflicting units without choosing one
// arbitrary side of an ambiguous author bridge. Existing groups are indivisible.
// Indices refer to members, and root is the initiating record, not a preference.
func CompatibleSubset(members []Metadata, groups []int64, root int) []int {
	n := len(members)
	if n == 0 || n > 64 || len(groups) != n || root < 0 || root >= n {
		return nil
	}
	sets := map[uint64]bool{}
	authors := make([][]string, n)
	isbn := make([]string, n)
	covers := make([][]bool, n)
	direct := make([][]bool, n)
	for i, m := range members {
		authors[i] = authorKeys(m.Authors)
		isbn[i] = ISBN(m.ISBN)
	}
	for i := range members {
		covers[i] = make([]bool, n)
		direct[i] = make([]bool, n)
		for j := range members {
			covers[i][j] = subset(authors[j], authors[i])
			direct[i][j] = Compare(members[i], members[j]).Work
		}
	}
	// An incomplete record sharing a known ISBN is not a competing witness
	// against the fuller author evidence for that ISBN. A namesake without an
	// ISBN cannot veto a strong group merely by sharing its first author.
	required := make([]uint64, n)
	for j := range members {
		for k := range members {
			if isbn[j] != "" && isbn[j] == isbn[k] && direct[j][k] && covers[k][j] && !covers[j][k] {
				required[j] |= uint64(1) << k
			}
		}
	}
	for i := range members {
		people := authors[i]
		if len(people) == 0 && len(members[i].SourceFamilies) == 0 {
			continue
		}
		var supported uint64
		for j, other := range members {
			strongEnough := true
			for k := range members {
				if required[j]&(uint64(1)<<k) != 0 && !covers[i][k] {
					strongEnough = false
					break
				}
			}
			if other.ISBN != "!conflict" && direct[i][j] && covers[i][j] && strongEnough {
				supported |= uint64(1) << j
			}
		}
		// Do not take a compatible individual out of a contradictory managed group.
		for _, group := range groups {
			if group == 0 {
				continue
			}
			var unit uint64
			for k, candidate := range groups {
				if candidate == group {
					unit |= uint64(1) << k
				}
			}
			if supported&unit != unit {
				supported &^= unit
			}
		}
		if supported == 0 {
			continue
		}
		selected := []Metadata{}
		for j, m := range members {
			if supported&(uint64(1)<<j) != 0 {
				selected = append(selected, m)
			}
		}
		if GroupCompatible(selected) {
			sets[supported] = true
		}
	}
	var intersection uint64
	first := true
	maximalSets := []uint64{}
	for set := range sets {
		maximal := true
		for other := range sets {
			if set != other && set&other == set {
				maximal = false
				break
			}
		}
		if !maximal {
			continue
		}
		maximalSets = append(maximalSets, set)
		if set&(uint64(1)<<root) == 0 {
			continue
		}
		if first {
			intersection = set
			first = false
		} else {
			intersection &= set
		}
	}
	for _, set := range maximalSets {
		if set&(uint64(1)<<root) == 0 {
			intersection &^= set
		}
	}
	var out []int
	var selected []Metadata
	for j, m := range members {
		if intersection&(uint64(1)<<j) != 0 {
			out = append(out, j)
			selected = append(selected, m)
		}
	}
	if len(out) < 2 || !GroupCompatible(selected) {
		return nil
	}
	return out
}
