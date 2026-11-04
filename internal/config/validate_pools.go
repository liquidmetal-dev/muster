package config

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// poolPath names a pool by name when it has one, else by index.
func poolPath(i int, p *Pool) string {
	if p.Name != "" {
		return "pools[" + strconv.Quote(p.Name) + "]"
	}
	return "pools[" + strconv.Itoa(i) + "]"
}

func validatePools(cfg *Config, errs *errorList) {
	if len(cfg.Pools) == 0 {
		errs.add("pools", "at least one pool is required")
		return
	}
	names := map[string]int{}
	for i := range cfg.Pools {
		p := &cfg.Pools[i]
		pp := poolPath(i, p)
		switch {
		case p.Name == "":
			errs.add(pp+".name", "required")
		case !IsDNSLabel(p.Name):
			errs.add(pp+".name", msgDNSLabel)
		default:
			if j, dup := names[p.Name]; dup {
				errs.add("pools["+strconv.Itoa(i)+"].name", "duplicate of pools[%d]", j)
			} else {
				names[p.Name] = i
			}
		}
		validateScope(pp+".scope", p.Scope, errs)
		validateLabels(pp+".labels", p.Labels, errs)
		if p.Size < 1 {
			errs.add(pp+".size", "must be at least 1")
		}
		if p.MaxConcurrent < 1 {
			errs.add(pp+".max_concurrent", "must be at least 1")
		}
		validateHosts(pp+".flintlock_hosts", p.FlintlockHosts, errs)
		validateStrategy(pp+".replenishment_strategy", p, errs)
		validateTemplateNetwork(pp+".microvm_template", p, errs)
		if p.MaxSessionDuration <= 0 {
			errs.add(pp+".max_session_duration", "must be positive")
		}
		if !isLocalPath(p.WorkFolder) {
			errs.add(pp+".work_folder", "must be a relative path inside the runner directory")
		}
		if p.Name != "" && len(RunnerNamePrefix(cfg.Battery.Namespace, p.Name))+SessionIDLen > maxRunnerName {
			errs.add(pp+".name", `runner names "muster-<namespace>-<pool>-<%d-character session id>" must be at most %d characters; shorten the pool name or battery.namespace`, SessionIDLen, maxRunnerName)
		}
	}
}

func validateScope(path string, s Scope, errs *errorList) {
	if (s.Org != "") == (s.Repo != "") {
		errs.add(path, "exactly one of org or repo must be set")
		return
	}
	if s.Org != "" && !isOwnerPart(s.Org) {
		errs.add(path+".org", `must be an owner name without "/" or whitespace`)
	}
	if s.Repo != "" && !isOwnerName(s.Repo) {
		errs.add(path+".repo", "must be owner/name")
	}
	if s.RunnerGroup != "" && s.Org == "" {
		errs.add(path+".runner_group", "only valid with org")
	}
}

func validateLabels(path string, labels []string, errs *errorList) {
	if len(labels) == 0 {
		errs.add(path, "required")
		return
	}
	seen := map[string]int{}
	hasSelfHosted := false
	for i, l := range labels {
		ip := path + "[" + strconv.Itoa(i) + "]"
		if l == "" || strings.ContainsAny(l, ",") || strings.IndexFunc(l, unicode.IsSpace) >= 0 {
			errs.add(ip, "must not be empty or contain whitespace or commas")
			continue
		}
		if l == "self-hosted" {
			hasSelfHosted = true
		}
		key := strings.ToLower(l)
		if j, dup := seen[key]; dup {
			errs.add(ip, "duplicate of labels[%d]", j)
			continue
		}
		seen[key] = i
	}
	if !hasSelfHosted {
		errs.add(path, `must include "self-hosted"`)
	}
}

func validateHosts(path string, hosts []string, errs *errorList) {
	if len(hosts) == 0 {
		errs.add(path, "required")
		return
	}
	seen := map[string]int{}
	for i, h := range hosts {
		ip := path + "[" + strconv.Itoa(i) + "]"
		if h == "" {
			errs.add(ip, "must not be empty")
			continue
		}
		if j, dup := seen[h]; dup {
			errs.add(ip, "duplicate of flintlock_hosts[%d]", j)
			continue
		}
		seen[h] = i
	}
}

func isLocalPath(p string) bool {
	return p != "" && filepath.IsLocal(p)
}

func validateStrategy(path string, p *Pool, errs *errorList) {
	if p.ReplenishmentStrategy == nil {
		return
	}
	s := p.ReplenishmentStrategy
	switch s.Type {
	case StrategyMinSizeThreshold:
		switch {
		case s.MinSize == 0:
			errs.add(path+".min_size", "required for min_size_threshold")
		case s.MinSize < 1 || s.MinSize > p.Size:
			errs.add(path+".min_size", "must be between 1 and size")
		}
	case StrategyImmediateOnLease, StrategyReplaceOnDelete:
		if s.MinSize != 0 {
			errs.add(path+".min_size", "only valid with min_size_threshold")
		}
	default:
		errs.add(path+".type", "must be one of %s, %s, %s", StrategyImmediateOnLease, StrategyMinSizeThreshold, StrategyReplaceOnDelete)
	}
}

// validateTemplateNetwork rejects what battery rejects for a
// DELETE_AND_REPLACE pool that can hold more than one VM: a fixed MAC or
// static address would be copied to every VM.
func validateTemplateNetwork(path string, p *Pool, errs *errorList) {
	if p.Template == nil || (p.Size <= 1 && p.Strategy().Type != StrategyImmediateOnLease) {
		return
	}
	const remedy = "every VM in the pool would get the same value; leave it unset unless size is 1 and replenishment_strategy.type is not immediate_on_lease"
	for i, iface := range p.Template.GetInterfaces() {
		field := ""
		switch {
		case iface.GetGuestMac() != "":
			field = "guest_mac"
		case iface.GetAddress() != nil:
			field = "address"
		default:
			continue
		}
		errs.add(path, "interfaces[%d].%s: %s", i, field, remedy)
		return
	}
}
