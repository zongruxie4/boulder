package ratelimits

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/letsencrypt/boulder/config"
	"github.com/letsencrypt/boulder/core"
	"github.com/letsencrypt/boulder/identifier"
	"github.com/letsencrypt/boulder/strictyaml"
)

// errLimitDisabled indicates that the limit name specified is valid but is not
// currently configured.
var errLimitDisabled = errors.New("limit disabled")

// LimitConfig defines the exportable configuration for a rate limit or a rate
// limit override, without a `limit`'s internal fields.
//
// The zero value of this struct is invalid, because some of the fields must be
// greater than zero.
type LimitConfig struct {
	// Burst specifies maximum concurrent allowed requests at any given time. It
	// must be greater than zero.
	Burst int64

	// Count is the number of requests allowed per period. It must be greater
	// than zero.
	Count int64

	// Period is the duration of time in which the count (of requests) is
	// allowed. It must be greater than zero.
	Period config.Duration
}

type LimitConfigs map[string]*LimitConfig

// Limit defines the configuration for a rate limit or a rate limit override.
//
// The zero value of this struct is invalid, because some of the fields must be
// greater than zero. It and several of its fields are exported to support admin
// tooling used during the migration from overrides.yaml to the overrides
// database table.
type Limit struct {
	// Burst specifies maximum concurrent allowed requests at any given time. It
	// must be greater than zero.
	Burst int64

	// Count is the number of requests allowed per period. It must be greater
	// than zero.
	Count int64

	// Period is the duration of time in which the count (of requests) is
	// allowed. It must be greater than zero.
	Period config.Duration

	// Name is the name of the limit. It must be one of the Name enums defined
	// in this package.
	Name Name

	// Comment is an optional field that can be used to provide additional
	// context for an override. It is not used for default limits.
	Comment string

	// emissionInterval is the interval, in nanoseconds, at which tokens are
	// added to a bucket (period / count). This is also the steady-state rate at
	// which requests can be made without being denied even once the burst has
	// been exhausted. This is precomputed to avoid doing the same calculation
	// on every request.
	emissionInterval int64

	// burstOffset is the duration of time, in nanoseconds, it takes for a
	// bucket to go from empty to full (burst * (period / count)). This is
	// precomputed to avoid doing the same calculation on every request.
	burstOffset int64

	// isOverride is true if the limit is an override.
	isOverride bool
}

// precompute calculates the emissionInterval and burstOffset for the limit.
func (l *Limit) precompute() {
	l.emissionInterval = l.Period.Nanoseconds() / l.Count
	l.burstOffset = l.emissionInterval * l.Burst
}

func ValidateLimit(l *Limit) error {
	if l.Burst <= 0 {
		return fmt.Errorf("invalid burst '%d', must be > 0", l.Burst)
	}
	if l.Count <= 0 {
		return fmt.Errorf("invalid count '%d', must be > 0", l.Count)
	}
	if l.Period.Duration <= 0 {
		return fmt.Errorf("invalid period '%s', must be > 0", l.Period)
	}
	return nil
}

type Limits map[string]*Limit

// loadDefaults marshals the defaults YAML file at path into a map of limits.
func loadDefaults(path string) (LimitConfigs, error) {
	lm := make(LimitConfigs)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	err = strictyaml.Unmarshal(data, &lm)
	if err != nil {
		return nil, err
	}
	return lm, nil
}

type overrideYAML struct {
	LimitConfig `yaml:",inline"`
	// Ids is a list of ids that this override applies to.
	Ids []struct {
		Id string `yaml:"id"`
		// Comment is an optional field that can be used to provide additional
		// context for the override.
		Comment string `yaml:"comment,omitempty"`
	} `yaml:"ids"`
}

type overridesYAML []map[string]overrideYAML

// loadOverrides marshals the YAML file at path into a map of overrides.
func loadOverrides(path string) (overridesYAML, error) {
	ov := overridesYAML{}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	err = strictyaml.Unmarshal(data, &ov)
	if err != nil {
		return nil, err
	}
	return ov, nil
}

// parseOverrideNameId is broken out for ease of testing.
func parseOverrideNameId(key string) (Name, string, error) {
	if !strings.Contains(key, ":") {
		// Avoids a potential panic in strings.SplitN below.
		return Unknown, "", fmt.Errorf("invalid override %q, must be formatted 'name:id'", key)
	}
	nameAndId := strings.SplitN(key, ":", 2)
	nameStr := nameAndId[0]
	if nameStr == "" {
		return Unknown, "", fmt.Errorf("empty name in override %q, must be formatted 'name:id'", key)
	}

	name, ok := StringToName[nameStr]
	if !ok {
		return Unknown, "", fmt.Errorf("unrecognized name %q in override limit %q, must be one of %v", nameStr, key, LimitNames)
	}
	id := nameAndId[1]
	if id == "" {
		return Unknown, "", fmt.Errorf("empty id in override %q, must be formatted 'name:id'", key)
	}
	return name, id, nil
}

// parseOverrideNameEnumId is like parseOverrideNameId, but it expects the
// key to be formatted as 'name:id', where 'name' is a Name enum string and 'id'
// is a string identifier. It returns an error if either part is missing or invalid.
func parseOverrideNameEnumId(key string) (Name, string, error) {
	if !strings.Contains(key, ":") {
		// Avoids a potential panic in strings.SplitN below.
		return Unknown, "", fmt.Errorf("invalid override %q, must be formatted 'name:id'", key)
	}
	nameStrAndId := strings.SplitN(key, ":", 2)
	if len(nameStrAndId) != 2 {
		return Unknown, "", fmt.Errorf("invalid override %q, must be formatted 'name:id'", key)
	}

	nameInt, err := strconv.Atoi(nameStrAndId[0])
	if err != nil {
		return Unknown, "", fmt.Errorf("invalid name %q in override limit %q, must be an integer", nameStrAndId[0], key)
	}
	name := Name(nameInt)
	if !name.isValid() {
		return Unknown, "", fmt.Errorf("invalid name %q in override limit %q, must be one of %v", nameStrAndId[0], key, LimitNames)

	}
	id := nameStrAndId[1]
	if id == "" {
		return Unknown, "", fmt.Errorf("empty id in override %q, must be formatted 'name:id'", key)
	}
	return name, id, nil
}

// parseOverrideLimits validates a YAML list of override limits. It must be
// formatted as a list of maps, where each map has a single key representing the
// limit name and a value that is a map containing the limit fields and an
// additional 'ids' field that is a list of ids that this override applies to.
func parseOverrideLimits(newOverridesYAML overridesYAML) (Limits, error) {
	parsed := make(Limits)

	for _, ov := range newOverridesYAML {
		for k, v := range ov {
			name, ok := StringToName[k]
			if !ok {
				return nil, fmt.Errorf("unrecognized name %q in override limit, must be one of %v", k, LimitNames)
			}

			for _, entry := range v.Ids {
				id := entry.Id
				err := validateIdForName(name, id)
				if err != nil {
					return nil, fmt.Errorf(
						"validating name %s and id %q for override limit %q: %w", name, id, k, err)
				}

				// We interpret and compute the override values for two rate
				// limits, since they're not nice to ask for in a config file.
				switch name {
				case CertificatesPerDomain:
					// Convert IP addresses to their covering /32 (IPv4) or /64
					// (IPv6) prefixes in CIDR notation.
					ip, err := netip.ParseAddr(id)
					if err == nil {
						prefix, err := coveringIPPrefix(name, ip)
						if err != nil {
							return nil, fmt.Errorf(
								"computing prefix for IP address %q: %w", id, err)
						}
						id = prefix.String()
					}
				case CertificatesPerFQDNSet:
					// Compute the hash of a comma-separated list of identifier
					// values.
					id = fmt.Sprintf("%x", core.HashIdentifiers(identifier.FromStringSlice(strings.Split(id, ","))))
				}

				lim := &Limit{
					Burst:      v.Burst,
					Count:      v.Count,
					Period:     v.Period,
					Name:       name,
					Comment:    entry.Comment,
					isOverride: true,
				}
				lim.precompute()

				err = ValidateLimit(lim)
				if err != nil {
					return nil, fmt.Errorf("validating override limit %q: %w", k, err)
				}

				parsed[joinWithColon(name.EnumString(), id)] = lim
			}
		}
	}
	return parsed, nil
}

// parseDefaultLimits validates a map of default limits and rekeys it by 'Name'.
func parseDefaultLimits(newDefaultLimits LimitConfigs) (Limits, error) {
	parsed := make(Limits)

	for k, v := range newDefaultLimits {
		name, ok := StringToName[k]
		if !ok {
			return nil, fmt.Errorf("unrecognized name %q in default limit, must be one of %v", k, LimitNames)
		}

		lim := &Limit{
			Burst:  v.Burst,
			Count:  v.Count,
			Period: v.Period,
			Name:   name,
		}

		err := ValidateLimit(lim)
		if err != nil {
			return nil, fmt.Errorf("parsing default limit %q: %w", k, err)
		}

		lim.precompute()
		parsed[name.EnumString()] = lim
	}
	return parsed, nil
}

type limitRegistry struct {
	// defaults stores default limits by 'name'.
	defaults Limits

	// overrides stores override limits by 'name:id'.
	overrides Limits
}

func newLimitRegistryFromFiles(defaults, overrides string) (*limitRegistry, error) {
	defaultsData, err := loadDefaults(defaults)
	if err != nil {
		return nil, err
	}

	if overrides == "" {
		return newLimitRegistry(defaultsData, nil)
	}

	overridesData, err := loadOverrides(overrides)
	if err != nil {
		return nil, err
	}

	return newLimitRegistry(defaultsData, overridesData)
}

func newLimitRegistry(defaults LimitConfigs, overrides overridesYAML) (*limitRegistry, error) {
	regDefaults, err := parseDefaultLimits(defaults)
	if err != nil {
		return nil, err
	}

	regOverrides, err := parseOverrideLimits(overrides)
	if err != nil {
		return nil, err
	}

	return &limitRegistry{
		defaults:  regDefaults,
		overrides: regOverrides,
	}, nil
}

// getLimit returns the limit for the specified by name and bucketKey, name is
// required, bucketKey is optional. If bucketkey is empty, the default for the
// limit specified by name is returned. If no default limit exists for the
// specified name, errLimitDisabled is returned.
func (l *limitRegistry) getLimit(name Name, bucketKey string) (*Limit, error) {
	if !name.isValid() {
		// This should never happen. Callers should only be specifying the limit
		// Name enums defined in this package.
		return nil, fmt.Errorf("specified name enum %q, is invalid", name)
	}
	if bucketKey != "" {
		// Check for override.
		ol, ok := l.overrides[bucketKey]
		if ok {
			return ol, nil
		}
	}
	dl, ok := l.defaults[name.EnumString()]
	if ok {
		return dl, nil
	}
	return nil, errLimitDisabled
}

// LoadOverridesByBucketKey loads the overrides YAML at the supplied path,
// parses it with the existing helpers, and returns the resulting limits map
// keyed by "<name>:<id>". This function is exported to support admin tooling
// used during the migration from overrides.yaml to the overrides database
// table.
func LoadOverridesByBucketKey(path string) (Limits, error) {
	ovs, err := loadOverrides(path)
	if err != nil {
		return nil, err
	}
	return parseOverrideLimits(ovs)
}

// DumpOverrides writes the provided overrides to CSV at the supplied path. Each
// override is written as a single row, one per ID. Rows are sorted in the
// following order:
//   - Name    (ascending)
//   - Count   (descending)
//   - Burst   (descending)
//   - Period  (ascending)
//   - Comment (ascending)
//   - ID      (ascending)
//
// This function supports admin tooling that routinely exports the overrides
// table for investigation or auditing.
func DumpOverrides(path string, overrides Limits) error {
	type row struct {
		name    string
		id      string
		count   int64
		burst   int64
		period  string
		comment string
	}

	var rows []row
	for bucketKey, limit := range overrides {
		name, id, err := parseOverrideNameEnumId(bucketKey)
		if err != nil {
			return err
		}

		rows = append(rows, row{
			name:    name.String(),
			id:      id,
			count:   limit.Count,
			burst:   limit.Burst,
			period:  limit.Period.Duration.String(),
			comment: limit.Comment,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		// Sort by limit name in ascending order.
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		// Sort by count in descending order (higher counts first).
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		// Sort by burst in descending order (higher bursts first).
		if rows[i].burst != rows[j].burst {
			return rows[i].burst > rows[j].burst
		}
		// Sort by period in ascending order (shorter durations first).
		if rows[i].period != rows[j].period {
			return rows[i].period < rows[j].period
		}
		// Sort by comment in ascending order.
		if rows[i].comment != rows[j].comment {
			return rows[i].comment < rows[j].comment
		}
		// Sort by ID in ascending order.
		return rows[i].id < rows[j].id
	})

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	err = w.Write([]string{"name", "id", "count", "burst", "period", "comment"})
	if err != nil {
		return err
	}

	for _, r := range rows {
		err := w.Write([]string{r.name, r.id, strconv.FormatInt(r.count, 10), strconv.FormatInt(r.burst, 10), r.period, r.comment})
		if err != nil {
			return err
		}
	}
	w.Flush()

	return w.Error()
}
