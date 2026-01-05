package widgets

import (
	"fmt"
	"image"
	"log"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	tui "github.com/gizak/termui/v3"
	rw "github.com/mattn/go-runewidth"
	psNet "github.com/shirou/gopsutil/v3/net"

	ui "github.com/xxxserxxx/gotop/v4/termui"
	"github.com/xxxserxxx/gotop/v4/utils"
)

const (
	// NetInterfaceAll enables all network interfaces
	NetInterfaceAll = "all"
	// NetInterfaceVpn is the VPN interface
	NetInterfaceVpn = "tun0"

	_netDownArrow = "▼"
	_netUpArrow   = "▲"
)

type NetWidget struct {
	*ui.SparklineGroup
	updateInterval time.Duration

	// used to calculate recent network activity
	totalBytesRecv uint64
	totalBytesSent uint64
	NetInterface   []string
	sentMetric     *metrics.Counter
	recvMetric     *metrics.Counter
	Mbps           bool
	// NetTitleStats controls whether compact RX/TX rates are rendered in the widget
	// title bar when the widget is too short to show per-line Title2 stats.
	NetTitleStats bool

	baseTitle    string
	recentRxRate string
	recentTxRate string
}

func padLeftToWidth(s string, w int) string {
	sw := rw.StringWidth(s)
	if sw >= w {
		return s
	}
	return strings.Repeat(" ", w-sw) + s
}

// formatHeaderRate ensures:
// - the icon is rendered AFTER the rate (e.g. "12.34 KB/s ▼")
// - exactly one space between the rate and the icon
// - any "fixed width" left-padding that exists in rate is preserved at the start.
func formatHeaderRate(arrow, rate string) string {
	trimmed := strings.TrimLeft(rate, " ")
	padLen := len(rate) - len(trimmed)
	pad := strings.Repeat(" ", padLen)
	return pad + trimmed + " " + arrow
}

// TODO: state:merge #169 % option for network use (jrswab/networkPercentage)
func NewNetWidget(netInterface string) *NetWidget {
	recvSparkline := ui.NewSparkline()
	recvSparkline.Data = []int{}

	sentSparkline := ui.NewSparkline()
	sentSparkline.Data = []int{}

	spark := ui.NewSparklineGroup(recvSparkline, sentSparkline)
	self := &NetWidget{
		SparklineGroup: spark,
		updateInterval: time.Second,
		NetInterface:   strings.Split(netInterface, ","),
	}
	self.baseTitle = tr.Value("widget.label.net")
	if netInterface != "all" {
		self.baseTitle = tr.Value("widget.label.netint", netInterface)
	}
	self.Title = self.baseTitle

	self.update()

	go func() {
		for range time.NewTicker(self.updateInterval).C {
			self.Lock()
			self.update()
			self.Unlock()
		}
	}()

	return self
}

// Draw overrides SparklineGroup.Draw so we can adjust the title when the widget is too small
// to display the per-line RX/s and TX/s stats.
func (net *NetWidget) Draw(buf *tui.Buffer) {
	// SparklineGroup.Draw only renders Title2 when Inner.Dy() > 6.
	// When it can't be shown, render those stats right-aligned in the widget header instead.
	net.Title = net.baseTitle
	net.SparklineGroup.Draw(buf)

	// Only draw the compact RX/TX rates in the header when the per-line Title2 can't be displayed.
	if !net.NetTitleStats || net.Inner.Dy() > 6 || net.recentRxRate == "" || net.recentTxRate == "" {
		return
	}

	// Right-align the RX/TX rate string similar to how the process widget draws its location.
	// Keep the icon glued to the rate; any fixed-width padding is handled inside the formatted rate.
	txGroup := formatHeaderRate(_netUpArrow, net.recentTxRate)
	rxGroup := formatHeaderRate(_netDownArrow, net.recentRxRate)

	// RX first, then TX; keep a single space between the two groups.
	right := fmt.Sprintf(" %s %s ", rxGroup, txGroup)

	rightW := rw.StringWidth(right)
	rightEdge := net.Max.X - 2
	minStart := net.Min.X + 2 + rw.StringWidth(net.Title) + 1
	startX := net.Max.X - rightW - 2

	// If the widget is too narrow, trim the right-side string to avoid overlapping the title.
	if startX < minStart {
		availW := rightEdge - minStart + 1
		if availW <= 0 {
			return
		}
		right = tui.TrimString(right, availW)
		rightW = rw.StringWidth(right)
		startX = rightEdge - rightW + 1
	}

	buf.SetString(right, net.TitleStyle, image.Pt(startX, net.Min.Y))
}

func (net *NetWidget) EnableMetric() {
	net.recvMetric = metrics.NewCounter(makeName("net", "recv"))
	net.sentMetric = metrics.NewCounter(makeName("net", "sent"))
}

func (net *NetWidget) update() {
	interfaces, err := psNet.IOCounters(true)
	if err != nil {
		log.Println(tr.Value("widget.net.err.netactivity", err.Error()))
		return
	}

	var totalBytesRecv uint64
	var totalBytesSent uint64
	interfaceMap := make(map[string]bool)
	// Default behaviour
	interfaceMap[NetInterfaceAll] = true
	interfaceMap[NetInterfaceVpn] = false
	// Build a map with wanted status for each interfaces.
	for _, iface := range net.NetInterface {
		if strings.HasPrefix(iface, "!") {
			interfaceMap[strings.TrimPrefix(iface, "!")] = false
		} else {
			// if we specify a wanted interface, remove capture on all.
			delete(interfaceMap, NetInterfaceAll)
			interfaceMap[iface] = true
		}
	}
	for _, _interface := range interfaces {
		wanted, ok := interfaceMap[_interface.Name]
		if wanted && ok { // Simple case
			totalBytesRecv += _interface.BytesRecv
			totalBytesSent += _interface.BytesSent
		} else if ok { // Present but unwanted
			continue
		} else if interfaceMap[NetInterfaceAll] { // Capture other
			totalBytesRecv += _interface.BytesRecv
			totalBytesSent += _interface.BytesSent
		}
	}

	var recentBytesRecv uint64
	var recentBytesSent uint64

	if net.totalBytesRecv != 0 { // if this isn't the first update
		recentBytesRecv = totalBytesRecv - net.totalBytesRecv
		recentBytesSent = totalBytesSent - net.totalBytesSent

		if int(recentBytesRecv) < 0 {
			v := fmt.Sprintf("%d", recentBytesRecv)
			log.Println(tr.Value("widget.net.err.negvalrecv", v))
			// recover from error
			recentBytesRecv = 0
		}
		if int(recentBytesSent) < 0 {
			v := fmt.Sprintf("%d", recentBytesSent)
			log.Printf(tr.Value("widget.net.err.negvalsent", v))
			// recover from error
			recentBytesSent = 0
		}

		net.Lines[0].Data = append(net.Lines[0].Data, int(recentBytesRecv))
		net.Lines[1].Data = append(net.Lines[1].Data, int(recentBytesSent))
		if net.sentMetric != nil {
			net.sentMetric.Add(int(recentBytesSent))
			net.recvMetric.Add(int(recentBytesRecv))
		}
	}

	// used in later calls to update
	net.totalBytesRecv = totalBytesRecv
	net.totalBytesSent = totalBytesSent

	rx, tx := "RX/s", "TX/s"
	if net.Mbps {
		rx, tx = "mbps", "mbps"
	}

	title1Format := " %s: %9.1f %2s/s"
	if net.Mbps {
		title1Format = " %s: %11.3f %2s"
	}

	specs := []struct {
		idx         int
		label       string
		rateLabel   string
		total       uint64
		recent      uint64
		compactDest *string
	}{
		{idx: 0, label: "RX", rateLabel: rx, total: totalBytesRecv, recent: recentBytesRecv, compactDest: &net.recentRxRate},
		{idx: 1, label: "TX", rateLabel: tx, total: totalBytesSent, recent: recentBytesSent, compactDest: &net.recentTxRate},
	}

	// render widget titles
	for _, s := range specs {
		totalConverted, unitTotal := utils.ConvertBytes(s.total)

		var recentConverted float64
		var unitRecent string
		if net.Mbps {
			recentConverted = float64(s.recent) * 0.000008
			unitRecent = ""
		} else {
			recentConverted, unitRecent = utils.ConvertBytes(s.recent)
		}

		net.Lines[s.idx].Title1 = fmt.Sprintf(title1Format, s.rateLabel, recentConverted, unitRecent)
		net.Lines[s.idx].Title2 = fmt.Sprintf(" %s %s: %5.1f %s", tr.Value("total"), s.label, totalConverted, unitTotal)

		// Keep a compact formatted rate for the widget title (used when Title2 is hidden).
		if net.Mbps {
			*s.compactDest = fmt.Sprintf("%.3f mbps", recentConverted)
		} else {
			*s.compactDest = fmt.Sprintf("%.2f %s/s", recentConverted, unitRecent)
		}
	}
}
