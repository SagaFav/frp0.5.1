// Copyright 2018 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatedier/frp/pkg/util/myutil"

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/auth"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/version"
)

const (
	CfgFileTypeIni = iota
	CfgFileTypeCmd
)

var (
	cfgFile        string
	cfgDir         string
	showVersion    bool
	rootServerAddr string
	rootServerPort int
	rootToken      string
	removeIni      bool
	enableAuth     bool
	socksport      int

	serverAddr      string
	user            string
	protocol        string
	token           string
	logLevel        string
	logFile         string
	logMaxDays      int
	disableLogColor bool
	dnsServer       string

	proxyName          string
	localIP            string
	localPort          int
	remotePort         int
	useEncryption      bool
	useCompression     bool
	bandwidthLimit     string
	bandwidthLimitMode string
	customDomains      string
	subDomain          string
	httpUser           string
	httpPwd            string
	locations          string
	hostHeaderRewrite  string
	role               string
	sk                 string
	multiplexer        string
	serverName         string
	bindAddr           string
	bindPort           int

	tlsEnable     bool
	tlsServerName string
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file of frpc")
	rootCmd.PersistentFlags().StringVarP(&cfgDir, "config_dir", "", "", "config directory, run one frpc service for each file in config directory")
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "version of frpc")
	rootCmd.PersistentFlags().StringVarP(&rootServerAddr, "server_addr", "s", "", "frp server's address")
	//rootCmd.PersistentFlags().IntVarP(&rootServerPort, "server_port", "p", 7000, "frp server's port")
	rootCmd.PersistentFlags().BoolVarP(&removeIni, "remove", "", false, "remove ini file after init")
	rootCmd.PersistentFlags().StringVarP(&rootToken, "token", "t", "", "auth token")
	rootCmd.PersistentFlags().BoolVarP(&enableAuth, "auth", "", true, "enable socks auth")
	//rootCmd.PersistentFlags().IntVarP(&socksport, "socks_port","",1080,"socksport")
}

func RegisterCommonFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&serverAddr, "server_addr", "s", "127.0.0.1:7000", "frp server's address")
	cmd.PersistentFlags().StringVarP(&user, "user", "u", "", "user")
	cmd.PersistentFlags().StringVarP(&protocol, "protocol", "p", "tcp", "tcp, kcp, quic, websocket, wss")
	cmd.PersistentFlags().StringVarP(&token, "token", "t", "", "auth token")
	cmd.PersistentFlags().StringVarP(&logLevel, "log_level", "", "info", "log level")
	cmd.PersistentFlags().StringVarP(&logFile, "log_file", "", "console", "console or file path")
	cmd.PersistentFlags().IntVarP(&logMaxDays, "log_max_days", "", 3, "log file reversed days")
	cmd.PersistentFlags().BoolVarP(&disableLogColor, "disable_log_color", "", false, "disable log color in console")
	cmd.PersistentFlags().BoolVarP(&tlsEnable, "tls_enable", "", true, "enable frpc tls")
	cmd.PersistentFlags().StringVarP(&tlsServerName, "tls_server_name", "", "", "specify the custom server name of tls certificate")
	cmd.PersistentFlags().StringVarP(&dnsServer, "dns_server", "", "", "specify dns server instead of using system default one")
}

var rootCmd = &cobra.Command{
	Use:   "frpc",
	Short: "frpc is the client of frp (https://github.com/fatedier/frp)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Println(version.Full())
			return nil
		}

		// If cfgDir is not empty, run multiple frpc service for each config file in cfgDir.
		// Note that it's only designed for testing. It's not guaranteed to be stable.
		if cfgDir != "" {
			_ = runMultipleClients(cfgDir)
			return nil
		}

		// Do not show command usage here.
		err := runClient(cfgFile, rootServerAddr, rootServerPort, rootToken, removeIni, enableAuth)
		if err != nil {
			os.Exit(1)
		}
		return nil
	},
}

func runMultipleClients(cfgDir string) error {
	var wg sync.WaitGroup
	err := filepath.WalkDir(cfgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		wg.Add(1)
		time.Sleep(time.Millisecond)
		go func() {
			defer wg.Done()
			err := runClient(path, rootServerAddr, rootServerPort, rootToken, removeIni, enableAuth)
			if err != nil {
				fmt.Printf("frpc service error for config file [%s]\n", path)
			}
		}()
		return nil
	})
	wg.Wait()
	return err
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func handleTermSignal(svr *client.Service) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	svr.GracefulClose(500 * time.Millisecond)
}

func parseClientCommonCfgFromCmd() (cfg config.ClientCommonConf, err error) {
	cfg = config.GetDefaultClientConf()

	ipStr, portStr, err := net.SplitHostPort(serverAddr)
	if err != nil {
		err = fmt.Errorf("invalid server_addr: %v", err)
		return
	}

	cfg.ServerAddr = ipStr
	cfg.ServerPort, err = strconv.Atoi(portStr)
	if err != nil {
		err = fmt.Errorf("invalid server_addr: %v", err)
		return
	}

	cfg.User = user
	cfg.Protocol = protocol
	cfg.LogLevel = logLevel
	cfg.LogFile = logFile
	cfg.LogMaxDays = int64(logMaxDays)
	cfg.DisableLogColor = disableLogColor
	cfg.DNSServer = dnsServer

	// Only token authentication is supported in cmd mode
	cfg.ClientConfig = auth.GetDefaultClientConf()
	cfg.Token = token
	cfg.TLSEnable = tlsEnable
	cfg.TLSServerName = tlsServerName

	cfg.Complete()
	if err = cfg.Validate(); err != nil {
		err = fmt.Errorf("parse config error: %v", err)
		return
	}
	return
}

func runClient(cfgFilePath string, rootServerAddr string, rootServerPort int, rootToken string, removeIni bool, enableAuth bool) error {
	_, _, remote_Port, _ := AESCBCDecrypt(rootServerAddr)
	cfg, pxyCfgs, visitorCfgs, err := config.ParseClientConfig(cfgFilePath, remote_Port)

	if err != nil {
		fmt.Println(err)
		return err
	}

	if cfgFilePath == "" {
		if rootServerAddr != "" {
			ip, port, _, _ := AESCBCDecrypt(rootServerAddr)
			cfg.ServerAddr = ip
			cfg.ServerPort = port
		} else {
			err = fmt.Errorf("server_addr must be specified when use default config file")
			log.Error(err.Error())
			return err
		}
		//这个是token 判断是否能连上frps的关键
		if rootToken != "" {
			cfg.Token = rootToken
		}
		//这个是这个socks连接的账号密码
		if enableAuth {
			conf := pxyCfgs["socks5"].(*config.TCPProxyConf)
			conf.PluginParams = make(map[string]string)
			conf.PluginParams["plugin_user"] = myutil.RandStr(6)
			conf.PluginParams["plugin_passwd"] = myutil.RandStr(12)
		}
	} else {
		if !strings.HasPrefix(cfgFilePath, "http://") && !strings.HasPrefix(cfgFilePath, "https://") {
			if removeIni {
				os.Remove(cfgFilePath)
				log.Warn("remove", cfgFilePath, "success")
			}
		}
	}

	return startService(cfg, pxyCfgs, visitorCfgs, cfgFilePath)
}

func startService(
	cfg config.ClientCommonConf,
	pxyCfgs map[string]config.ProxyConf,
	visitorCfgs map[string]config.VisitorConf,
	cfgFile string,
) (err error) {
	log.InitLog(cfg.LogWay, cfg.LogFile, cfg.LogLevel,
		cfg.LogMaxDays, cfg.DisableLogColor)

	if cfgFile == "" {
		log.Info("start frpc service for default config file")
		defer log.Info("frpc service for default config file stopped")
	} else if strings.HasPrefix(cfgFile, "http://") || strings.HasPrefix(cfgFile, "https://") {
		log.Info("start frpc service for remote config file [%s]", cfgFile)
		defer log.Info("frpc service for remote config file [%s] stopped", cfgFile)
	} else {
		log.Info("start frpc service for local config file [%s]", cfgFile)
		defer log.Info("frpc service for local config file [%s] stopped", cfgFile)
	}

	svr, errRet := client.NewService(cfg, pxyCfgs, visitorCfgs, cfgFile)
	if errRet != nil {
		err = errRet
		return
	}

	shouldGracefulClose := cfg.Protocol == "kcp" || cfg.Protocol == "quic"
	// Capture the exit signal if we use kcp or quic.
	if shouldGracefulClose {
		go handleTermSignal(svr)
	}

	_ = svr.Run(context.Background())
	return
}

// AESCBCDecrypt 使用 AES 解密字符串密文（CBC 模式）
func AESCBCDecrypt(ciphertextBase64 string) (string, int, int, error) {
	// base64 解码密文
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return "", 0, 0, err
	}

	// 将密钥和 IV 转换为字节数组
	key := []byte("1234561234561234") // 密钥
	iv := []byte("1234561234561234")  // 初始化向量

	// 创建 AES 分组
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", 0, 0, err
	}

	// 创建解密器
	decrypter := cipher.NewCBCDecrypter(block, iv)

	// 解密数据
	plaintext := make([]byte, len(ciphertext))
	decrypter.CryptBlocks(plaintext, ciphertext)

	// 去除填充
	plaintext, err = PKCS7Unpad(plaintext)
	if err != nil {
		return "", 0, 0, err
	}

	// 解析解密后的字符串
	parts := strings.Split(string(plaintext), ":")
	if len(parts) != 3 {
		return "", 0, 0, fmt.Errorf("invalid plaintext format")
	}
	a := parts[0]
	b, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, 0, err
	}
	c, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, 0, err
	}

	return a, b, c, nil
}

// PKCS7Unpad 去除 PKCS#7 填充
// func PKCS7Unpad(data []byte) ([]byte, error) {
// 	length := len(data)
// 	unpadding := int(data[length-1])
// 	if unpadding > length {
// 		return nil, fmt.Errorf("invalid padding")
// 	}
// 	return data[:length-unpadding], nil
// }

func PKCS7Unpad(data []byte) ([]byte, error) {
	length := len(data)
	if length == 0 {
		return nil, fmt.Errorf("data is empty")
	}
	unpadding := int(data[length-1])
	if unpadding > length || unpadding <= 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	return data[:length-unpadding], nil
}
