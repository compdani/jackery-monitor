// Package ecoflow ports the EcoFlow private-cloud integration: app login,
// MQTT telemetry for Delta 3 / Delta 3 Max Plus, protobuf decode/encode,
// the device registry + Siseli overlay, reconnect-window settings and
// credential normalisation.
//
// Auth and MQTT certification follow tolwi/hassio-ecoflow-cloud private_api
// (Apache-2.0). The protobuf bindings (package efpb) are generated from a
// .proto reconstructed from the vendored Python descriptor.
//
// There is no PocketBase dependency. Persistence is behind small Store
// interfaces, HTTP behind HTTPDoer, MQTT behind MQTTTransport.
package ecoflow
