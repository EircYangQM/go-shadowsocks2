package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/ini.v1"
)

var config struct {
	Verbose    bool
	UDPTimeout time.Duration
	TCPCork    bool
}

type ServerConfig struct {
	Address    string
	Cipher     string
	Password   string
	Plugin     string
	PluginOpts string
}

type ProxyConfig struct {
	Port          int
	ProxyAddress  string
	User          string
	Password      string
}

func main() {
	//orginal()
	modified()
}

func modified() {
	var flags struct {
		ConfigFile         string
		ServerListFile     string
	}

	flag.StringVar(&flags.ConfigFile, "c", "", "ShadowSocks configuration file.")
	flag.StringVar(&flags.ServerListFile, "s", "", "The server list file.")
	flag.Parse()
	config := new(ServerConfig)

	if flags.ConfigFile != "" {
		cfg, err := ini.Load(flags.ConfigFile)
		if err != nil {
			fmt.Printf("Fail to read file: %v", err)
			os.Exit(1)
		}

		config.Address = cfg.Section("server").Key("address").String()
		config.Cipher = cfg.Section("server").Key("cipher").String()
		config.Password = cfg.Section("server").Key("password").String()
		config.Plugin = cfg.Section("server").Key("plugin").String()
		config.PluginOpts = cfg.Section("server").Key("pluginopts").String()
	}

	var proxies []ProxyConfig
	if flags.ServerListFile != "" {
		f, err := os.Open(flags.ServerListFile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			proxyString := scanner.Text()
			s := strings.Split(proxyString, "#")
			if len(s) != 2 {
				fmt.Printf("Invalid Server Format: %v", proxyString)
				continue
			}

			proxy := new(ProxyConfig)
			proxy.Port, err = strconv.Atoi(s[0])
			if err != nil {
				fmt.Printf("Invalid Server Format: %v, %v", proxyString, err)
				continue
			}

			addr, err := url.Parse(s[1])
			if err != nil || addr.Scheme != "socks5" {
				fmt.Printf("Invalid Server Format: %v, %v", proxyString, err)
				continue
			}

			if addr.User != nil {
				proxy.User = addr.User.Username()
				proxy.Password, _ = addr.User.Password()
				proxy.ProxyAddress = addr.Host
			}

			proxy.ProxyAddress = addr.Host
			proxies = append(proxies, *proxy)
		}

		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	}

	for _, proxy := range proxies {
		go startSingleServer(config, &proxy)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	killPlugin()
}

func orginal() {
	var flags struct {
		Client     string
		Server     string
		Cipher     string
		Key        string
		Password   string
		Keygen     int
		Socks      string
		RedirTCP   string
		RedirTCP6  string
		TCPTun     string
		UDPTun     string
		UDPSocks   bool
		UDP        bool
		TCP        bool
		Plugin     string
		PluginOpts string
		Proxy      string
	}

	flag.BoolVar(&config.Verbose, "verbose", false, "verbose mode")
	flag.StringVar(&flags.Cipher, "cipher", "AEAD_CHACHA20_POLY1305", "available ciphers: "+strings.Join(core.ListCipher(), " "))
	flag.StringVar(&flags.Key, "key", "", "base64url-encoded key (derive from password if empty)")
	flag.IntVar(&flags.Keygen, "keygen", 0, "generate a base64url-encoded random key of given length in byte")
	flag.StringVar(&flags.Password, "password", "", "password")
	flag.StringVar(&flags.Server, "s", "", "server listen address or url")
	flag.StringVar(&flags.Client, "c", "", "client connect address or url")
	flag.StringVar(&flags.Socks, "socks", "", "(client-only) SOCKS listen address")
	flag.BoolVar(&flags.UDPSocks, "u", false, "(client-only) Enable UDP support for SOCKS")
	flag.StringVar(&flags.RedirTCP, "redir", "", "(client-only) redirect TCP from this address")
	flag.StringVar(&flags.RedirTCP6, "redir6", "", "(client-only) redirect TCP IPv6 from this address")
	flag.StringVar(&flags.TCPTun, "tcptun", "", "(client-only) TCP tunnel (laddr1=raddr1,laddr2=raddr2,...)")
	flag.StringVar(&flags.UDPTun, "udptun", "", "(client-only) UDP tunnel (laddr1=raddr1,laddr2=raddr2,...)")
	flag.StringVar(&flags.Plugin, "plugin", "", "Enable SIP003 plugin. (e.g., v2ray-plugin)")
	flag.StringVar(&flags.PluginOpts, "plugin-opts", "", "Set SIP003 plugin options. (e.g., \"server;tls;host=mydomain.me\")")
	flag.BoolVar(&flags.UDP, "udp", false, "(server-only) enable UDP support")
	flag.BoolVar(&flags.TCP, "tcp", true, "(server-only) enable TCP support")
	flag.StringVar(&flags.Proxy, "proxy", "", "(server-only) the proxy which server use. For example, socks5://username:password@127.0.0.1:1081")
	flag.BoolVar(&config.TCPCork, "tcpcork", false, "coalesce writing first few packets")
	flag.DurationVar(&config.UDPTimeout, "udptimeout", 5*time.Minute, "UDP tunnel timeout")
	flag.Parse()

	if flags.Keygen > 0 {
		key := make([]byte, flags.Keygen)
		io.ReadFull(rand.Reader, key)
		fmt.Println(base64.URLEncoding.EncodeToString(key))
		return
	}

	if flags.Client == "" && flags.Server == "" {
		flag.Usage()
		return
	}

	var key []byte
	if flags.Key != "" {
		k, err := base64.URLEncoding.DecodeString(flags.Key)
		if err != nil {
			log.Fatal(err)
		}
		key = k
	}

	if flags.Client != "" { // client mode
		addr := flags.Client
		cipher := flags.Cipher
		password := flags.Password
		var err error

		if strings.HasPrefix(addr, "ss://") {
			addr, cipher, password, err = parseURL(addr)
			if err != nil {
				log.Fatal(err)
			}
		}

		udpAddr := addr

		ciph, err := core.PickCipher(cipher, key, password)
		if err != nil {
			log.Fatal(err)
		}

		if flags.Plugin != "" {
			addr, err = startPlugin(flags.Plugin, flags.PluginOpts, addr, false)
			if err != nil {
				log.Fatal(err)
			}
		}

		if flags.UDPTun != "" {
			for _, tun := range strings.Split(flags.UDPTun, ",") {
				p := strings.Split(tun, "=")
				go udpLocal(p[0], udpAddr, p[1], ciph.PacketConn)
			}
		}

		if flags.TCPTun != "" {
			for _, tun := range strings.Split(flags.TCPTun, ",") {
				p := strings.Split(tun, "=")
				go tcpTun(p[0], addr, p[1], ciph.StreamConn)
			}
		}

		if flags.Socks != "" {
			socks.UDPEnabled = flags.UDPSocks
			go socksLocal(flags.Socks, addr, ciph.StreamConn)
			if flags.UDPSocks {
				go udpSocksLocal(flags.Socks, udpAddr, ciph.PacketConn)
			}
		}

		if flags.RedirTCP != "" {
			go redirLocal(flags.RedirTCP, addr, ciph.StreamConn)
		}

		if flags.RedirTCP6 != "" {
			go redir6Local(flags.RedirTCP6, addr, ciph.StreamConn)
		}
	}

	if flags.Server != "" { // server mode
		addr := flags.Server
		cipher := flags.Cipher
		password := flags.Password
		proxy := flags.Proxy
		var err error

		if strings.HasPrefix(addr, "ss://") {
			addr, cipher, password, err = parseURL(addr)
			if err != nil {
				log.Fatal(err)
			}
		}

		udpAddr := addr

		if flags.Plugin != "" {
			addr, err = startPlugin(flags.Plugin, flags.PluginOpts, addr, true)
			if err != nil {
				log.Fatal(err)
			}
		}

		ciph, err := core.PickCipher(cipher, key, password)
		if err != nil {
			log.Fatal(err)
		}

		if flags.UDP {
			go udpRemote(udpAddr, proxy, password, ciph.PacketConn)
		}
		if flags.TCP {
			go tcpRemote(addr, proxy, "", "", ciph.StreamConn)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	killPlugin()
}

func parseURL(s string) (addr, cipher, password string, err error) {
	u, err := url.Parse(s)
	if err != nil {
		return
	}

	addr = u.Host
	if u.User != nil {
		cipher = u.User.Username()
		password, _ = u.User.Password()
	}
	return
}

func startSingleServer(config *ServerConfig, proxy *ProxyConfig) {
	addr := config.Address
	cipher := config.Cipher
	password := config.Password
	var err error

	if config.Plugin != "" {
		addr, err = startPlugin(config.Plugin, config.PluginOpts, addr, true)
		if err != nil {
			log.Fatal(err)
		}
	}

	var key []byte
	ciph, err := core.PickCipher(cipher, key, password)
	if err != nil {
		log.Fatal(err)
	}

	go tcpRemote(":"+strconv.Itoa(proxy.Port), proxy.ProxyAddress, proxy.User, proxy.Password, ciph.StreamConn)
}
