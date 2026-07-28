package proxy

type roundRobin struct {
	endpoints []Endpoint
	next      int
}

func (r *roundRobin) add(endpoint Endpoint) {
	for idx, existing := range r.endpoints {
		if existing.URL == endpoint.URL {
			r.endpoints[idx] = endpoint
			return
		}
	}
	r.endpoints = append(r.endpoints, endpoint)
}

func (r *roundRobin) remove(url string) {
	for idx, existing := range r.endpoints {
		if existing.URL != url {
			continue
		}
		r.endpoints = append(r.endpoints[:idx], r.endpoints[idx+1:]...)
		if len(r.endpoints) == 0 {
			r.next = 0
			return
		}
		if r.next >= len(r.endpoints) {
			r.next = 0
		}
		return
	}
}

func (r *roundRobin) pick() (Endpoint, bool) {
	return r.pickWhere(func(Endpoint) bool { return true })
}

func (r *roundRobin) pickWhere(eligible func(Endpoint) bool) (Endpoint, bool) {
	if len(r.endpoints) == 0 {
		return Endpoint{}, false
	}
	for offset := 0; offset < len(r.endpoints); offset++ {
		idx := (r.next + offset) % len(r.endpoints)
		endpoint := r.endpoints[idx]
		if eligible(endpoint) {
			r.next = (idx + 1) % len(r.endpoints)
			return endpoint, true
		}
	}
	return Endpoint{}, false
}

func (r *roundRobin) len() int {
	return len(r.endpoints)
}
