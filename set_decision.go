package main

// DecisionSet - decision map of array object for ref purpose.
type DecisionSet map[uint64]ArrayIntSet

// Remove - delete the decision.
func (a *DecisionSet) Remove(decision uint64, id int32) {
	if v, ok := (*a)[decision]; ok {
		v = v.Del(id)

		if len(v) == 0 {
			delete(*a, decision)

			return
		}

		(*a)[decision] = v
	}
}

// Insert - add the decision.
func (a *DecisionSet) Insert(decision uint64, id int32) {
	v, ok := (*a)[decision]
	if !ok {
		v = make(ArrayIntSet, 0, 1)
	}

	(*a)[decision] = v.Add(id)
}
