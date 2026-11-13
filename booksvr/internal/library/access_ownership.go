package library

import "talebook/booksvr/internal/access"

func accessBookOwnership(files []AccessFileSnapshot) (string, bool) {
	owner := ""
	for _, file := range files {
		if _, ok := accessOwnershipFacts(file.Facts, &owner, 0); !ok {
			return "", false
		}
	}
	return owner, true
}

// Extract ownership into the sparse overlay without losing private ancestry.
// Copies prevent one shared profile's evaluation from altering another's facts.
func accessOwnershipFacts(f access.FileFacts, owner *string, depth int) (access.FileFacts, bool) {
	if depth > 16 || len(f.Parents) > 32 {
		return f, false
	}
	if f.Private {
		if f.PrivateOwner == "" || (*owner != "" && *owner != f.PrivateOwner) {
			return f, false
		}
		*owner = f.PrivateOwner
		f.Private = false
	}
	parents := make([]access.FileFacts, len(f.Parents))
	for i, p := range f.Parents {
		var valid bool
		parents[i], valid = accessOwnershipFacts(p, owner, depth+1)
		if !valid {
			return f, false
		}
	}
	f.Parents = parents
	return f, true
}
