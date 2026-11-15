package tesla

import (
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// Stop is one navigation destination.
type Stop struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label,omitempty"`
}

// Validate rejects impossible coordinates.
func (s Stop) Validate() error {
	if math.IsNaN(s.Lat) || math.IsNaN(s.Lng) || s.Lat < -90 || s.Lat > 90 || s.Lng < -180 || s.Lng > 180 {
		return errors.New("invalid coordinates")
	}
	if s.Lat == 0 && s.Lng == 0 {
		return errors.New("coordinates 0,0")
	}
	return nil
}

// ValidateStops checks a batch: 1 to MaxStops valid stops.
func ValidateStops(stops []Stop) error {
	if len(stops) == 0 {
		return errors.New("at least one stop required")
	}
	if len(stops) > MaxStops {
		return fmt.Errorf("at most %d stops per batch", MaxStops)
	}
	for i, s := range stops {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("stop %d: %w", i+1, err)
		}
	}
	return nil
}

// RemoteNavTripOrder_E values (community tesla-protocol car_server.proto).
const (
	OrderReplace int32 = 0
	OrderPrepend int32 = 1
	OrderAppend  int32 = 2
)

// Single-stop placement in the car's existing trip (RemoteNavTripOrder_E).
const (
	PlaceAppend  = "append"
	PlacePrepend = "prepend"
)

// OrderForPlacement maps a single-stop placement to its order value.
func OrderForPlacement(p string) (int32, error) {
	switch p {
	case "", PlaceAppend:
		return OrderAppend, nil
	case PlacePrepend:
		return OrderPrepend, nil
	}
	return 0, fmt.Errorf("position must be %s or %s", PlaceAppend, PlacePrepend)
}

// OrderFor returns the `order` value for the stop at index i (0-based).
func OrderFor(mode string, i int) int32 {
	switch mode {
	case OrderPosition:
		return int32(i + 1)
	case OrderOneThree:
		if i == 0 {
			return 1
		}
		return 3
	}
	if i == 0 {
		return OrderReplace
	}
	return OrderAppend
}

// Field numbers in car_server.proto. Tesla's published vehicle-command proto
// does not define the navigation messages; these come from the community
// tesla-protocol package (Action.vehicleAction = 2,
// VehicleAction.navigationGpsRequest = 53, NavigationGpsRequest{lat=1 double,
// lon=2 double, order=3 enum}). Tesla's proto neither uses nor reserves 53.
const (
	fieldActionVehicleAction     = 2
	fieldVehicleActionNavGPS     = 53
	fieldNavGPSLat               = 1
	fieldNavGPSLon               = 2
	fieldNavGPSOrder             = 3
	fieldVehicleActionNavWaypnts = 90
	fieldNavWaypointsWaypoints   = 1
)

// EncodeGPSAction builds the serialized carserver.Action for a
// navigation_gps_request, ready for the infotainment domain.
func EncodeGPSAction(s Stop, order int32) []byte {
	var nav []byte
	nav = protowire.AppendTag(nav, fieldNavGPSLat, protowire.Fixed64Type)
	nav = protowire.AppendFixed64(nav, math.Float64bits(s.Lat))
	nav = protowire.AppendTag(nav, fieldNavGPSLon, protowire.Fixed64Type)
	nav = protowire.AppendFixed64(nav, math.Float64bits(s.Lng))
	if order != 0 { // proto3: the default value is omitted on the wire
		nav = protowire.AppendTag(nav, fieldNavGPSOrder, protowire.VarintType)
		nav = protowire.AppendVarint(nav, uint64(int64(order)))
	}
	return wrapVehicleAction(fieldVehicleActionNavGPS, nav)
}

// EncodeWaypointsAction builds a navigation_waypoints_request carrying a
// "refId:<place id>,..." string (the Fleet API's documented format).
func EncodeWaypointsAction(waypoints string) []byte {
	var msg []byte
	msg = protowire.AppendTag(msg, fieldNavWaypointsWaypoints, protowire.BytesType)
	msg = protowire.AppendString(msg, waypoints)
	return wrapVehicleAction(fieldVehicleActionNavWaypnts, msg)
}

func wrapVehicleAction(field protowire.Number, inner []byte) []byte {
	var va []byte
	va = protowire.AppendTag(va, field, protowire.BytesType)
	va = protowire.AppendBytes(va, inner)
	var action []byte
	action = protowire.AppendTag(action, fieldActionVehicleAction, protowire.BytesType)
	action = protowire.AppendBytes(action, va)
	return action
}
