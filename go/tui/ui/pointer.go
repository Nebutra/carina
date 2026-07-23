package ui

import "sort"

type routedRegion struct {
	region HitRegion
	order  int
}

type PointerRouter struct {
	generation uint64
	regions    []routedRegion
	hovered    HitID
	hoverOwner ComponentID
}

func (p *PointerRouter) Invalidate() {
	p.generation = 0
	p.regions = p.regions[:0]
	p.hovered = ""
	p.hoverOwner = ""
}

func (p *PointerRouter) Publish(generation uint64, root Node) {
	p.generation = generation
	p.regions = p.regions[:0]
	order := 0
	var visit func(Node, int)
	visit = func(node Node, parentZ int) {
		z := parentZ + node.Z
		for _, hit := range node.Hit {
			if hit.Owner == "" {
				hit.Owner = node.ID
			}
			hit.Z += z
			p.regions = append(p.regions, routedRegion{region: hit, order: order})
			order++
		}
		for _, child := range node.Children {
			visit(child, z)
		}
	}
	visit(root, 0)
	sort.SliceStable(p.regions, func(i, j int) bool {
		if p.regions[i].region.Z != p.regions[j].region.Z {
			return p.regions[i].region.Z > p.regions[j].region.Z
		}
		return p.regions[i].order > p.regions[j].order
	})
	if p.hovered != "" && !p.hasHit(p.hovered) {
		p.hovered = ""
		p.hoverOwner = ""
	}
}

func (p *PointerRouter) Route(generation uint64, event PointerEvent) (PointerEvent, bool) {
	if generation == 0 || generation != p.generation {
		return event, false
	}
	if event.Kind == PointerLeave {
		p.hovered = ""
		p.hoverOwner = ""
		return event, true
	}
	for _, candidate := range p.regions {
		region := candidate.region
		if region.Disabled || !region.Bounds.Contains(event.X, event.Y) {
			continue
		}
		copy := region
		event.Hit = &copy
		event.LocalX = event.X - region.Bounds.X
		event.LocalY = event.Y - region.Bounds.Y
		if event.Kind == PointerMove {
			p.hovered = region.ID
			p.hoverOwner = region.Owner
		}
		return event, true
	}
	if event.Kind == PointerMove {
		p.hovered = ""
		p.hoverOwner = ""
	}
	return event, false
}

func (p *PointerRouter) Hovered() HitID            { return p.hovered }
func (p *PointerRouter) HoveredOwner() ComponentID { return p.hoverOwner }
func (p *PointerRouter) Generation() uint64        { return p.generation }
func (p *PointerRouter) HasHoverRegions() bool {
	for _, candidate := range p.regions {
		if candidate.region.Kind == HitHover {
			return true
		}
	}
	return false
}

func (p *PointerRouter) hasHit(id HitID) bool {
	for _, candidate := range p.regions {
		if candidate.region.ID == id {
			return true
		}
	}
	return false
}
