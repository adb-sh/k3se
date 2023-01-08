package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Config is a flat configuration for an SSH connection.
type Config struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	User        string `yaml:"user"`
	Password    string `yaml:"password"`
	KeyFile     string `yaml:"key-file"`
	Key         string `yaml:"key"`
	Passphrase  string `yaml:"passphrase"`
	Fingerprint string `yaml:"fingerprint"`
}

// Client is an augmented SSH client.
type Client struct {
	*Options

	SSH  *ssh.Client
	SFTP *sftp.Client
}

// NewClient creates a new SSH client and a new SFTP client based
// on an SSH configuration and connects to it.
func NewClient(config *Config, options ...Option) (*Client, error) {
	opts, err := GetDefaultOptions().Apply(options...)
	if err != nil {
		return nil, err
	}

	// Create a new client.
	client := &Client{
		Options: opts,
	}

	// Set default connection options.
	if config.Port == 0 {
		config.Port = 22
	}
	if config.User == "" {
		config.User = "root"
	}

	normalizedConfig, err := client.normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	address := fmt.Sprintf("%s:%d", config.Host, config.Port)

	if client.Proxy != nil {
		// Create a TCP connection from the proxy host to the target.
		netConn, err := client.Proxy.SSH.Dial("tcp", address)
		if err != nil {
			return nil, err
		}

		targetConn, channel, req, err := ssh.NewClientConn(netConn, address, normalizedConfig)
		if err != nil {
			return nil, err
		}

		client.SSH = ssh.NewClient(targetConn, channel, req)
	} else {
		if client.SSH, err = ssh.Dial("tcp", address, normalizedConfig); err != nil {
			return nil, err
		}
	}

	// Prevent issues with SSH servers that do not permit SFTP.
	if !client.STFPDisabled {
		if client.SFTP, err = sftp.NewClient(client.SSH); err != nil {
			return nil, err
		}
	}

	return client, nil
}

// normalizeConfig creates a new client config that is compatible with the standard library.
func (client *Client) normalizeConfig(config *Config) (*ssh.ClientConfig, error) {
	// Load the private key. A key that is specified directly takes
	// precedence over a key file.
	key := config.Key
	if key == "" && config.KeyFile != "" {
		// Resolve the home directory if necessary.
		if config.KeyFile[0] == '~' {
			userInfo, err := user.Current()
			if err != nil {
				return nil, err
			}
			config.KeyFile = userInfo.HomeDir + config.KeyFile[1:]
		}

		keyBytes, err := os.ReadFile(config.KeyFile)
		if err != nil {
			return nil, err
		}
		key = string(keyBytes)
	}

	// Configure the authentication method, which may either be a
	// password, a private key or an encrypted private key. Please
	// note that a private key will always take precedence over a
	// password.
	var authMethod ssh.AuthMethod
	if key != "" {
		// Use passphrase to decrypt the private key.
		if config.Passphrase != "" {
			signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(config.Passphrase))
			if err != nil {
				return nil, err
			}
			authMethod = ssh.PublicKeys(signer)
		} else {
			signer, err := ssh.ParsePrivateKey([]byte(key))
			if err != nil {
				return nil, err
			}
			authMethod = ssh.PublicKeys(signer)
		}
	} else if config.Password != "" {
		// Fall back to password authentication.
		authMethod = ssh.Password(config.Password)
		client.Logger.Warn().Msg("Using password authentication is insecure!")
		client.Logger.Warn().Msg("Please consider using public key authentication!")
	} else {
		return nil, errors.New("no authentication method specified")
	}

	// Configure host key verification.
	var hostKeyCallback ssh.HostKeyCallback
	if config.Fingerprint != "" {
		hostKeyCallback = func(hostname string, remote net.Addr, pubKey ssh.PublicKey) error {
			fingerprint := ssh.FingerprintSHA256(pubKey)
			if config.Fingerprint != fingerprint {
				return fmt.Errorf("fingerprint mismatch: server fingerprint: %s", fingerprint)
			}
			return nil
		}
	} else {
		client.Logger.Warn().Msg("Skipping host key verification is insecure!")
		client.Logger.Warn().Msg("This allows for person-in-the-middle attacks!")
		client.Logger.Warn().Msg("Please consider using fingerprint verification!")
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	return &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCallback,
		User:            config.User,
		Timeout:         client.Timeout,
	}, nil
}

// Do executes a command on the remote host.
func (client *Client) Do(command Cmd) error {
	session, err := client.SSH.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	// Set the command to execute.
	session.Stdin = command.Stdin
	session.Stdout = command.Stdout
	session.Stderr = command.Stderr

	// Execute the command.
	return session.Run(command.String())
}

// Close closes the SFTP connection first as it
// piggy-backs on the SSH connection. After that
// the SSH connection of the client is closed.
func (client *Client) Close() error {
	if client.SFTP != nil {
		if err := client.SFTP.Close(); err != nil {
			return err
		}
	}

	if client.SSH != nil {
		if err := client.SSH.Close(); err != nil {
			return err
		}
	}

	return nil
}
