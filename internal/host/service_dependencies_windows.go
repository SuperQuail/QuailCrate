package host

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"quailcrate/internal/plugins"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type availableExports map[string][]v2.Export

func (exports availableExports) missing(m v1.Manifest) *v2.Dependency {
	for i := range m.Uses {
		dep := &m.Uses[i]
		if !dep.Optional && !slices.ContainsFunc(exports[dep.ID], func(e v2.Export) bool { return compatible(e, *dep) }) {
			return dep
		}
	}
	return nil
}

// A service can have several providers. Move a package to the runnable set as
// soon as each required service has *one* compatible provider already in it.
func resolvePackages(packages []plugins.Package) (ordered, pending []plugins.Package, exports availableExports) {
	exports = make(availableExports)
	pending = slices.Clone(packages)
	for len(pending) > 0 {
		next := pending[:0]
		for _, pkg := range pending {
			if pkg.Err == nil && exports.missing(pkg.Manifest) != nil {
				next = append(next, pkg)
				continue
			}
			ordered = append(ordered, pkg)
			if pkg.Err == nil {
				for _, e := range pkg.Manifest.Provides {
					exports[e.ID] = append(exports[e.ID], e)
				}
			}
		}
		progress := len(next) != len(pending)
		pending = next
		if !progress {
			break
		}
	}
	return
}

func dependencyOrder(packages []plugins.Package) []plugins.Package {
	ready, blocked, _ := resolvePackages(packages)
	return append(ready, blocked...)
}

func (h *Host) groundedServicesLocked() availableExports {
	var active []plugins.Package
	for _, r := range h.records {
		if r.generation != 0 {
			active = append(active, r.pkg)
		}
	}
	_, _, exports := resolvePackages(active)
	return exports
}

func (h *Host) requiredServices(m v1.Manifest) error {
	if !slices.ContainsFunc(m.Uses, func(d v2.Dependency) bool { return !d.Optional }) {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	exports := h.groundedServicesLocked()
	dep := exports.missing(m)
	if dep == nil {
		return nil
	}
	// Follow a deterministic unavailable edge so a dependent shows the whole
	// path to the missing service, disabled provider, or circular dependency.
	chain := []string{m.ID}
	seen := map[string]bool{m.ID: true}
	for {
		chain = append(chain, fmt.Sprintf("%s %s [%s]", dep.ID, dep.VersionRange, strings.Join(dep.Methods, ", ")))
		var candidates []string
		var incompatible []string
		for id, r := range h.records {
			for _, e := range r.pkg.Manifest.Provides {
				if compatible(e, *dep) {
					candidates = append(candidates, id)
					break
				}
				if e.ID == dep.ID {
					incompatible = append(incompatible, id+"@"+e.Version)
				}
			}
		}
		if len(candidates) == 0 {
			if len(incompatible) > 0 {
				sort.Strings(incompatible)
				return fmt.Errorf("required dependency %s: incompatible exports %s", strings.Join(chain, " -> "), strings.Join(incompatible, ", "))
			}
			return fmt.Errorf("required dependency %s: no installed provider", strings.Join(chain, " -> "))
		}
		sort.Strings(candidates)
		id := candidates[0]
		// Prefer explaining a blocked dependency to listing an unrelated disabled alternative.
		for _, p := range candidates {
			r := h.records[p]
			if r.blocked || r.generation != 0 {
				id = p
				break
			}
		}
		chain = append(chain, id)
		if seen[id] {
			return fmt.Errorf("required dependency cycle: %s", strings.Join(chain, " -> "))
		}
		seen[id] = true
		r := h.records[id]
		if r.blocked || r.generation != 0 {
			if next := exports.missing(r.pkg.Manifest); next != nil {
				dep = next
				continue
			}
		}
		return fmt.Errorf("required dependency %s: provider is %s", strings.Join(chain, " -> "), r.info.State)
	}
}

// Callers hold Host.lifecycle. Only retry desired instances that previously
// failed their dependency check. Failed initialization itself needs a rescan.
func (h *Host) reconcileServices() {
	records := make([]*record, 0, len(h.records))
	for _, r := range h.records {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].info.ID < records[j].info.ID })
	for {
		changed := false
		for _, r := range records {
			// A rescan can withdraw the old grants or find an explicit disable
			// while the instance is waiting for its provider.
			if r.blocked && !r.info.Enabled {
				r.blocked = false
			}
			missing := h.requiredServices(r.pkg.Manifest)
			switch {
			case r.generation != 0 && missing != nil:
				h.disable(r)
				r.blocked = true
				h.fail(r, missing)
				changed = true
			case r.blocked && missing == nil:
				h.enable(r, r.grants)
				changed = changed || r.generation != 0 || !r.blocked
			case r.blocked && missing != nil && r.info.Detail != missing.Error():
				h.fail(r, missing)
			}
		}
		if !changed {
			break
		}
	}
	for _, r := range records {
		if r.generation == 0 || len(r.pkg.Manifest.Uses) == 0 {
			continue
		}
		var versions []string
		h.mu.Lock()
		for _, d := range r.pkg.Manifest.Uses {
			eps, _ := h.candidatesLocked(d, "")
			for _, e := range eps {
				versions = append(versions, fmt.Sprintf("%s/%s/%d", d.ID, e.id, e.generation))
			}
		}
		h.mu.Unlock()
		signature := strings.Join(versions, ";")
		if r.serviceSignature != signature {
			r.serviceSignature = signature
			r.serial.Lock()
			h.refresh(r, r.generation)
			r.serial.Unlock()
		}
	}
}
