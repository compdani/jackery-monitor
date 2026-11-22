package routing

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// DirectionsTTL is how long a fetched road leg is reused. Short enough that
// traffic changes show up; long enough that redrawing the same trip (after
// an app restart, an out-of-order completion, a replan) does not re-buy
// every leg.
const DirectionsTTL = 20 * time.Minute

// maxCachedLegs bounds the cache; a full cache drops expired legs first and
// is cleared if that is not enough.
const maxCachedLegs = 4000

// departureBucket groups future departures so nearby ones share legs.
const departureBucket = 15 * time.Minute

type cachedLeg struct {
	leg     Leg
	expires time.Time
}

type dirCache struct {
	mu   sync.Mutex
	legs map[string]cachedLeg
}

// dirKey identifies one Directions request: both points (at the precision
// sent to Apple), toll avoidance and the departure bucket ("now" when none).
func dirKey(ctx context.Context, from, to Point) string {
	k := ll(from) + ">" + ll(to)
	if AvoidTollsFrom(ctx) {
		k += "|notolls"
	}
	if t, ok := DepartureFrom(ctx); ok {
		k += "|d" + strconv.FormatInt(t.Truncate(departureBucket).Unix(), 10)
	}
	return k
}

func (c *dirCache) get(key string, now time.Time) (Leg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.legs[key]
	if !ok {
		return Leg{}, false
	}
	if !now.Before(e.expires) {
		delete(c.legs, key)
		return Leg{}, false
	}
	return e.leg, true
}

func (c *dirCache) put(key string, leg Leg, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.legs == nil {
		c.legs = make(map[string]cachedLeg)
	}
	if len(c.legs) >= maxCachedLegs {
		for k, e := range c.legs {
			if !now.Before(e.expires) {
				delete(c.legs, k)
			}
		}
		if len(c.legs) >= maxCachedLegs {
			clear(c.legs)
		}
	}
	c.legs[key] = cachedLeg{leg: leg, expires: now.Add(DirectionsTTL)}
}
