package ipsec

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bronze1man/goStrongswanVici"
	"github.com/rancher/go-rancher-metadata/metadata"
	"github.com/rancher/ipsec/store"

	// "github.com/rancher/log"
	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const (
	reqID    = 1234
	reqIDStr = "1234"
	pskFile  = "psk.txt"
	pidFile  = "/var/run/charon.pid"

	// DefaultReplayWindowSize specifies the replay window size for charon
	DefaultReplayWindowSize = "1024"

	// DefaultIkeSaRekeyInterval specifies the default rekey interval for IKE_SA
	DefaultIkeSaRekeyInterval = "4h"

	// DefaultChildSaRekeyInterval specifies the default rekey interval for CHILD_SA
	DefaultChildSaRekeyInterval = "1h"
)

// Overlay is used to store information about the Overlay Network
type Overlay struct {
	sync.Mutex

	keyAttempt                map[string]bool
	hostAttempt               map[string]bool
	keys                      map[string]string
	hosts                     map[string]string
	templates                 Templates
	db                        store.Store
	mc                        metadata.Client
	psk                       string
	Blacklist                 []string
	ReplayWindowSize          string
	IPSecIkeSaRekeyInterval   string
	IPSecChildSaRekeyInterval string
}

// NewOverlay creates a new Overlay
func NewOverlay(configDir string, db store.Store, mc metadata.Client) *Overlay {
	return &Overlay{
		mc: mc,
		db: db,
		templates: Templates{
			ConfigDir: configDir,
		},
		keys:  map[string]string{},
		hosts: map[string]string{},
	}
}

// Start begins/starts the overlay network
func (o *Overlay) Start(launch bool, logFile string) {
	if launch {
		go runCharon(logFile)
	} else {
		go o.monitorCharon()
	}

	go o.mc.OnChange(5, o.onChangeNoError)

	if err := o.loadConns(); err != nil {
		log.Fatalf("Failed to load connections from charon: %v", err)
	}

}

func (o *Overlay) onChangeNoError(version string) {
	if err := o.Reload(); err != nil {
		log.Errorf("failed to reload overlay: %v", err)
	}
}

// Test ...
func Test() error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	if _, err := client.ListConns(""); err != nil {
		return err
	}

	return nil
}

func (o *Overlay) loadConns() error {
	o.Lock()
	defer o.Unlock()

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	conns, err := client.ListConns("")
	if err != nil {
		return err
	}

	o.hosts = map[string]string{}

	for _, conn := range conns {
		for k := range conn {
			if strings.HasPrefix(k, "conn-") {
				log.Infof("Found existing connection: %s", k)
				o.hosts[strings.TrimPrefix(k, "conn-")] = o.templates.Revision()
			}
		}
	}

	return nil
}

// Reload is used to refresh the state of the overlay network
func (o *Overlay) Reload() error {
	if err := o.db.Reload(); err != nil {
		return err
	}

	content, err := ioutil.ReadFile(path.Join(o.templates.ConfigDir, pskFile))
	if err != nil {
		return err
	}
	o.psk = strings.TrimSpace(string(content))

	return o.configure()
}

func (o *Overlay) monitorCharon() {
	pid := ""
	for {
		newPidBytes, err := ioutil.ReadFile(pidFile)
		if err != nil {
			log.Fatalf("Failed to read %s", pidFile)
		}
		newPid := strings.TrimSpace(string(newPidBytes))
		if pid == "" {
			pid = newPid
			log.Infof("Charon running PID: %s", pid)
		} else if pid != newPid {
			log.Fatalf("Charon restarted, old PID: %s, new PID: %s", pid, newPid)
		} else {
			o.Lock()
			if err := Test(); err != nil {
				log.Errorf("Killing charon due to: %v", err)
				o.killCharon(pid)
			}
			o.Unlock()
		}
		time.Sleep(2 * time.Second)
	}
}

func runCharon(logFile string) {
	// Ignore error
	os.Remove("/var/run/charon.vici")

	args := []string{}
	for _, i := range strings.Split("dmn|mgr|ike|chd|cfg|knl|net|asn|tnc|imc|imv|pts|tls|esp|lib", "|") {
		args = append(args, "--debug-"+i)
		if log.GetLevel().String() == "debug" {
			args = append(args, "3")
		} else {
			args = append(args, "0")
		}
	}

	cmd := exec.Command("charon", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if logFile != "" {
		output, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to log to file %s: %v", logFile, err)
		}
		defer output.Close()
		cmd.Stdout = output
		cmd.Stderr = output
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}

	log.Fatalf("charon exited: %v", cmd.Run())
}

func handleErr(firstErr, err error, fmt string, args ...interface{}) error {
	log.Errorf(fmt, args...)
	if firstErr != nil {
		return firstErr
	}
	return err
}

func (o *Overlay) configure() error {
	o.Lock()
	defer o.Unlock()
	log.Infof("Reconfiguring")

	if err := o.templates.Reload(); err != nil {
		return err
	}

	o.keyAttempt = map[string]bool{}
	o.hostAttempt = map[string]bool{}

	var firstErr error
	localHostIP := o.db.LocalHostIPAddress()
	hosts := map[string]bool{}

	policiesToAdd := map[string]netlink.XfrmPolicy{}
	existingPolicies, err := o.getRules()
	if err != nil {
		firstErr = handleErr(firstErr, err, "Failed to list rules for: %v", err)
	}

	if err := o.loadSharedKey(""); err != nil {
		firstErr = handleErr(firstErr, err, "Failed to load key for %any: %v", err)
	}

	for _, entry := range o.db.Entries() {
		if entry.Peer {
			if err := o.loadSharedKey(entry.IPAddress); err != nil {
				firstErr = handleErr(firstErr, err, "Failed to set PSK for peer agent %s: %v", entry.IPAddress, err)
			}
		}

		if localHostIP == entry.HostIPAddress {
			continue
		}
		if !hosts[entry.HostIPAddress] {
			if err := o.addHost(entry); err == nil {
				hosts[entry.HostIPAddress] = true
			} else {
				firstErr = handleErr(firstErr, err, "Failed to setup host %s: %v", entry.HostIPAddress, err)
			}
		}

		if err := o.addRules(entry, existingPolicies, policiesToAdd); err != nil {
			firstErr = handleErr(firstErr, err, "Failed to add rules for host %s, ip %s : %v", entry.HostIPAddress, entry.IPAddress, err)
		}
	}

	if firstErr == nil {
		firstErr = o.deletePolicies(existingPolicies)
	}

	if firstErr == nil {
		firstErr = o.addPolicies(policiesToAdd)
	}

	if firstErr == nil {
		firstErr = o.removeHosts()
		// Currently VICI doesn't support unloading keys
	}

	return firstErr
}

func (o *Overlay) killCharon(pid string) {
	pidNum, err := strconv.Atoi(pid)
	if err == nil {
		err = syscall.Kill(pidNum, syscall.SIGKILL)
	}

	if err != nil {
		log.Errorf("Can't kill %s: %v", pid, err)
	}
}

func (o *Overlay) deletePolicies(policies map[string]netlink.XfrmPolicy) error {
	var lastErr error
	for _, policy := range policies {
		if err := netlink.XfrmPolicyDel(&policy); err != nil {
			log.Errorf("Failed to delete policy: %+v, %v", policy, err)
			lastErr = err
		} else {
			log.Infof("Deleted policy: %+v", policy)
		}
	}
	return lastErr
}

func (o *Overlay) addPolicies(policies map[string]netlink.XfrmPolicy) error {
	var lastErr error
	for _, policy := range policies {
		if err := netlink.XfrmPolicyAdd(&policy); err != nil {
			log.Errorf("Failed to add policy: %+v, %v", policy, err)
			lastErr = err
		} else {
			log.Infof("Added policy: %+v", policy)
		}
	}
	return lastErr
}

func (o *Overlay) getRules() (map[string]netlink.XfrmPolicy, error) {
	policies := map[string]netlink.XfrmPolicy{}
	existing, err := netlink.XfrmPolicyList(0)
	if err != nil {
		return nil, err
	}

	for _, policy := range existing {
		if policy.Dir != netlink.XFRM_DIR_IN && policy.Dir != netlink.XFRM_DIR_FWD && policy.Dir != netlink.XFRM_DIR_OUT {
			continue
		}
		policies[toKey(&policy)] = policy
	}

	return policies, nil
}

func (o *Overlay) removeHosts() error {
	var firstErr error

	for k := range o.hosts {
		if !o.hostAttempt[k] {
			if err := o.removeHost(k); err != nil {
				firstErr = handleErr(firstErr, err, "Failed to add remove connection for host %s: %v", k, err)
			} else {
				log.Infof("Removed connection for %s", k)
				delete(o.hosts, k)
			}
		}
	}

	return firstErr
}

func (o *Overlay) removeHost(host string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	name := "conn-" + strings.Split(host, "/")[0]
	log.Infof("Removing connection for %s", name)
	return client.UnloadConn(&goStrongswanVici.UnloadConnRequest{
		Name: name,
	})
}

func getClient() (*goStrongswanVici.ClientConn, error) {
	var err error
	for i := 0; i < 3; i++ {
		var client *goStrongswanVici.ClientConn
		client, err = goStrongswanVici.NewClientConnFromDefaultSocket()
		if err == nil {
			return client, nil
		}

		if i > 0 {
			log.Errorf("Failed to connect to charon: %v", err)
		}
		time.Sleep(1 * time.Second)
	}

	return nil, err
}

func (o *Overlay) addHost(entry store.Entry) error {
	if err := o.loadSharedKey(entry.HostIPAddress); err != nil {
		return err
	}

	return o.addHostConnection(entry)
}

func (o *Overlay) loadSharedKey(ipAddress string) error {
	ipAddress = strings.Split(ipAddress, "/")[0]
	key := o.getPsk(ipAddress)

	o.keyAttempt[ipAddress] = true
	if o.keys[ipAddress] == key {
		log.Debugf("Key for %s already loaded", ipAddress)
		return nil
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	sharedKey := &goStrongswanVici.Key{
		Typ:    "IKE",
		Data:   key,
		Owners: []string{ipAddress},
	}

	err = client.LoadShared(sharedKey)
	if err != nil {
		log.Infof("Failed to load pre-shared key for %s: %v", ipAddress, err)
		return err
	}

	o.keys[ipAddress] = key
	log.Infof("Loaded pre-shared key for %s", ipAddress)
	return nil
}

func (o *Overlay) filterAlgos(algos []string) []string {
	ret := []string{}
	for _, algo := range algos {
		add := true
		for _, ignore := range o.Blacklist {
			if strings.HasPrefix(algo, ignore) {
				add = false
				break
			}
		}
		if add {
			ret = append(ret, algo)
		}
	}

	return ret
}

func (o *Overlay) addHostConnection(entry store.Entry) error {
	o.hostAttempt[entry.HostIPAddress] = true
	if o.hosts[entry.HostIPAddress] == o.templates.Revision() {
		log.Debugf("Connection already loaded for host %s", entry.HostIPAddress)
		return nil
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	childSAConf := o.templates.NewChildSaConf()
	childSAConf.ESPProposals = o.filterAlgos(childSAConf.ESPProposals)
	childSAConf.ReqID = reqIDStr
	childSAConf.RekeyTime = o.IPSecChildSaRekeyInterval
	if strings.Compare(entry.HostIPAddress, o.db.LocalHostIPAddress()) < 0 {
		childSAConf.RekeyTime = "8760h"
	}
	log.Infof("For entry: %v, using RekeyTime: %v", entry, childSAConf.RekeyTime)

	log.Debugf("Using ReplayWindowSize: %v", o.ReplayWindowSize)
	childSAConf.ReplayWindow = o.ReplayWindowSize

	ikeConf := o.templates.NewIkeConf()
	ikeConf.Proposals = o.filterAlgos(ikeConf.Proposals)
	ikeConf.RemoteAddrs = []string{entry.HostIPAddress}
	ikeConf.RekeyTime = o.IPSecIkeSaRekeyInterval
	if strings.Compare(entry.HostIPAddress, o.db.LocalHostIPAddress()) < 0 {
		ikeConf.RekeyTime = "8760h"
	}
	ikeConf.Children = map[string]goStrongswanVici.ChildSAConf{
		"child-" + entry.HostIPAddress: childSAConf,
	}

	name := fmt.Sprintf("conn-%s", entry.HostIPAddress)
	// Loading connections doesn't seem to be very reliable, can't get info
	// why it's failing though.
	for i := 0; i < 3; i++ {
		err = client.LoadConn(&map[string]goStrongswanVici.IKEConf{
			name: ikeConf,
		})
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Errorf("Failed loading connection %s: %v", name, err)
		return err
	}

	o.hosts[entry.HostIPAddress] = o.templates.Revision()
	log.Infof("Loaded connection: %v, %v, %v", name, ikeConf.Proposals, childSAConf.ESPProposals)

	return nil
}

func toKey(p *netlink.XfrmPolicy) string {
	buffer := bytes.Buffer{}
	buffer.WriteString(p.Dir.String())
	buffer.WriteRune('-')
	if p.Src != nil {
		buffer.WriteString(p.Src.String())
	}
	buffer.WriteRune('-')
	if p.Dst != nil {
		buffer.WriteString(p.Dst.String())
	}
	buffer.WriteRune('-')
	if len(p.Tmpls) > 0 {
		buffer.WriteString(p.Tmpls[0].Src.String())
		buffer.WriteRune('-')
		buffer.WriteString(p.Tmpls[0].Dst.String())
		buffer.WriteRune('-')
		buffer.WriteString(strconv.Itoa(p.Tmpls[0].Reqid))
	}

	return buffer.String()
}

func (o *Overlay) addRules(entry store.Entry, existingPolicies map[string]netlink.XfrmPolicy, policiesToAdd map[string]netlink.XfrmPolicy) error {
	localIP := net.ParseIP(o.db.LocalIPAddress())
	remoteHostIP := net.ParseIP(entry.HostIPAddress)

	_, localSubnet, err := net.ParseCIDR(o.db.LocalSubnet())
	if err != nil {
		return err
	}

	ip, _, err := net.ParseCIDR(entry.IPAddress)
	if err != nil {
		return err
	}

	_, ipDirectNet, err := net.ParseCIDR(fmt.Sprintf("%s/32", ip))
	if err != nil {
		return err
	}

	outPolicy := netlink.XfrmPolicy{
		Src:      localSubnet,
		Dst:      ipDirectNet,
		Dir:      netlink.XFRM_DIR_OUT,
		Priority: 10000,
		Tmpls: []netlink.XfrmPolicyTmpl{
			{
				Src:   localIP,
				Dst:   remoteHostIP,
				Proto: netlink.XFRM_PROTO_ESP,
				Mode:  netlink.XFRM_MODE_TUNNEL,
				Reqid: reqID,
			},
		},
	}
	inPolicy := netlink.XfrmPolicy{
		Src:      ipDirectNet,
		Dst:      localSubnet,
		Dir:      netlink.XFRM_DIR_IN,
		Priority: 10000,
		Tmpls: []netlink.XfrmPolicyTmpl{
			{
				Src:   remoteHostIP,
				Dst:   localIP,
				Proto: netlink.XFRM_PROTO_ESP,
				Mode:  netlink.XFRM_MODE_TUNNEL,
				Reqid: reqID,
			},
		},
	}
	fwdPolicy := netlink.XfrmPolicy{
		Src:      ipDirectNet,
		Dst:      localSubnet,
		Dir:      netlink.XFRM_DIR_FWD,
		Priority: 10000,
		Tmpls: []netlink.XfrmPolicyTmpl{
			{
				Src:   remoteHostIP,
				Dst:   localIP,
				Proto: netlink.XFRM_PROTO_ESP,
				Mode:  netlink.XFRM_MODE_TUNNEL,
				Reqid: reqID,
			},
		},
	}

	var lastErr error
	for _, policy := range []netlink.XfrmPolicy{outPolicy, inPolicy, fwdPolicy} {
		key := toKey(&policy)
		if _, ok := existingPolicies[key]; ok {
			delete(existingPolicies, key)
		} else {
			policiesToAdd[key] = policy
		}
	}

	return lastErr
}

func (o *Overlay) getPsk(hostIP string) string {
	return o.psk
}
