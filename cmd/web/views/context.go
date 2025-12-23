package views

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	cmdutils "git.gammaspectra.live/P2Pool/observer-cmd-utils/utils"
	"github.com/valyala/quicktemplate"
)

type GlobalRequestContext struct {
	IsOnion           bool
	DonationAddress   string
	SiteTitle         string
	NetServiceAddress string
	TorServiceAddress string
	BasePath          string
	Consensus         *sidechain.Consensus
	Pool              *cmdutils.PoolInfoResult
	Socials           struct {
		Irc struct {
			Title   string
			Link    string
			WebChat string
		}
		Matrix struct {
			Link string
		}
	}
	HexBuffer [types.HashSize * 2]byte
}

func (ctx *GlobalRequestContext) GetUrl(host string) string {
	return cmdutils.GetSiteUrlByHost(host, ctx.IsOnion)
}

// Path prepends the base path to a URL for proper routing in subdirectories
func (ctx *GlobalRequestContext) Path(url string) string {
	if ctx.BasePath == "" {
		return url
	}
	// Remove trailing slash from base path and leading slash from url if present
	basePath := ctx.BasePath
	if len(basePath) > 0 && basePath[len(basePath)-1] == '/' {
		basePath = basePath[:len(basePath)-1]
	}
	if len(url) > 0 && url[0] == '/' {
		return basePath + url
	}
	return basePath + "/" + url
}

// StreamPath is the quicktemplate streaming version of Path
func (ctx *GlobalRequestContext) StreamPath(qw *quicktemplate.Writer, url string) {
	qw.N().S(ctx.Path(url))
}
