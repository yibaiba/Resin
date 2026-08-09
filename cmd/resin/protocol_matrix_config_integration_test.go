//go:build integration

package main

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

type protocolMatrixConfig struct {
	inbound  map[string]any
	outbound map[string]any
}

type protocolMatrixCredentials struct {
	username string
	password string
	uuid     string
}

func newProtocolMatrixConfig(
	t *testing.T,
	protocol string,
	port int,
	certificates protocolMatrixCertificates,
) protocolMatrixConfig {
	t.Helper()
	credentials := newProtocolMatrixCredentials(t)
	serverTLS := map[string]any{
		"enabled":          true,
		"certificate_path": certificates.serverCertificatePath,
		"key_path":         certificates.serverKeyPath,
	}
	clientTLS := map[string]any{
		"enabled":          true,
		"server_name":      "localhost",
		"certificate_path": certificates.caCertificatePath,
	}

	switch protocol {
	case "socks":
		return socksMatrixConfig(port, credentials)
	case "shadowsocks":
		return shadowsocksMatrixConfig(port, credentials)
	case "vmess":
		return vmessMatrixConfig(port, credentials)
	case "vless":
		return vlessMatrixConfig(port, credentials)
	case "trojan":
		return trojanMatrixConfig(port, credentials, serverTLS, clientTLS)
	case "hysteria":
		return hysteriaMatrixConfig(port, credentials, serverTLS, clientTLS)
	case "hysteria2":
		return hysteria2MatrixConfig(port, credentials, serverTLS, clientTLS)
	case "tuic":
		return tuicMatrixConfig(port, credentials, serverTLS, clientTLS)
	default:
		t.Fatalf("unsupported protocol matrix case %q", protocol)
		return protocolMatrixConfig{}
	}
}

func socksMatrixConfig(port int, credentials protocolMatrixCredentials) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("socks", port, map[string]any{
			"users": []any{map[string]any{
				"username": credentials.username,
				"password": credentials.password,
			}},
		}),
		outbound: matrixOutbound("socks", port, map[string]any{
			"version":  "5",
			"username": credentials.username,
			"password": credentials.password,
		}),
	}
}

func shadowsocksMatrixConfig(port int, credentials protocolMatrixCredentials) protocolMatrixConfig {
	fields := map[string]any{
		"method":   "aes-128-gcm",
		"password": credentials.password,
	}
	return protocolMatrixConfig{
		inbound:  matrixInbound("shadowsocks", port, fields),
		outbound: matrixOutbound("shadowsocks", port, fields),
	}
}

func vmessMatrixConfig(port int, credentials protocolMatrixCredentials) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("vmess", port, map[string]any{
			"users": []any{map[string]any{"uuid": credentials.uuid}},
		}),
		outbound: matrixOutbound("vmess", port, map[string]any{
			"uuid": credentials.uuid, "security": "auto",
		}),
	}
}

func vlessMatrixConfig(port int, credentials protocolMatrixCredentials) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("vless", port, map[string]any{
			"users": []any{map[string]any{"uuid": credentials.uuid}},
		}),
		outbound: matrixOutbound("vless", port, map[string]any{
			"uuid": credentials.uuid,
		}),
	}
}

func trojanMatrixConfig(
	port int,
	credentials protocolMatrixCredentials,
	serverTLS map[string]any,
	clientTLS map[string]any,
) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("trojan", port, map[string]any{
			"users": []any{map[string]any{"password": credentials.password}},
			"tls":   serverTLS,
		}),
		outbound: matrixOutbound("trojan", port, map[string]any{
			"password": credentials.password,
			"tls":      clientTLS,
		}),
	}
}

func hysteriaMatrixConfig(
	port int,
	credentials protocolMatrixCredentials,
	serverTLS map[string]any,
	clientTLS map[string]any,
) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("hysteria", port, map[string]any{
			"up_mbps": 100, "down_mbps": 100,
			"users": []any{map[string]any{"auth_str": credentials.password}},
			"tls":   serverTLS,
		}),
		outbound: matrixOutbound("hysteria", port, map[string]any{
			"up_mbps": 100, "down_mbps": 100,
			"auth_str": credentials.password,
			"tls":      clientTLS,
		}),
	}
}

func hysteria2MatrixConfig(
	port int,
	credentials protocolMatrixCredentials,
	serverTLS map[string]any,
	clientTLS map[string]any,
) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("hysteria2", port, map[string]any{
			"users": []any{map[string]any{"password": credentials.password}},
			"tls":   serverTLS,
		}),
		outbound: matrixOutbound("hysteria2", port, map[string]any{
			"password": credentials.password,
			"tls":      clientTLS,
		}),
	}
}

func tuicMatrixConfig(
	port int,
	credentials protocolMatrixCredentials,
	serverTLS map[string]any,
	clientTLS map[string]any,
) protocolMatrixConfig {
	return protocolMatrixConfig{
		inbound: matrixInbound("tuic", port, map[string]any{
			"users": []any{map[string]any{
				"uuid": credentials.uuid, "password": credentials.password,
			}},
			"congestion_control": "bbr",
			"tls":                serverTLS,
		}),
		outbound: matrixOutbound("tuic", port, map[string]any{
			"uuid": credentials.uuid, "password": credentials.password,
			"congestion_control": "bbr",
			"udp_relay_mode":     "native",
			"tls":                clientTLS,
		}),
	}
}

func matrixInbound(protocol string, port int, fields map[string]any) map[string]any {
	result := copyMatrixFields(fields)
	result["type"] = protocol
	result["tag"] = "matrix-inbound"
	result["listen"] = "127.0.0.1"
	result["listen_port"] = port
	return result
}

func matrixOutbound(protocol string, port int, fields map[string]any) map[string]any {
	result := copyMatrixFields(fields)
	result["type"] = protocol
	result["tag"] = "matrix-outbound"
	result["server"] = "127.0.0.1"
	result["server_port"] = port
	return result
}

func copyMatrixFields(fields map[string]any) map[string]any {
	result := make(map[string]any, len(fields)+4)
	for key, value := range fields {
		result[key] = value
	}
	return result
}

func newProtocolMatrixCredentials(t *testing.T) protocolMatrixCredentials {
	t.Helper()
	return protocolMatrixCredentials{
		username: "user-" + randomMatrixHex(t, 4),
		password: randomMatrixHex(t, 16),
		uuid:     randomMatrixUUID(t),
	}
}

func randomMatrixHex(t *testing.T, byteCount int) string {
	t.Helper()
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate random matrix credential: %v", err)
	}
	return hex.EncodeToString(value)
}

func randomMatrixUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate random matrix UUID: %v", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32]
}
