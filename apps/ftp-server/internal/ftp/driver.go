package ftp

import (
	"crypto/tls"
	"errors"
	"log/slog"

	"github.com/fclairamb/ftpserverlib"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/vfs"
)

type MainDriver struct {
	cfg     config.Config
	store   *db.Store
	objects blob.Store
	logger  *slog.Logger
}

var errTLSNotConfigured = errors.New("TLS is not configured")

func NewMainDriver(cfg config.Config, store *db.Store, objects blob.Store, logger *slog.Logger) *MainDriver {
	return &MainDriver{
		cfg:     cfg,
		store:   store,
		objects: objects,
		logger:  logger,
	}
}

func (d *MainDriver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{
		ListenAddr:               d.cfg.ListenAddr(),
		PublicHost:               d.cfg.FTPPasvURL,
		PassiveTransferPortRange: &ftpserver.PortRange{Start: d.cfg.FTPPasvMin, End: d.cfg.FTPPasvMax},
		Banner:                   "Welcome to vfs-link FTP Server",
		DefaultTransferType:      ftpserver.TransferTypeBinary,
	}, nil
}

func (d *MainDriver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	d.logger.Info("FTP client connected", "client_id", cc.ID(), "remote", cc.RemoteAddr().String())
	return "Welcome to vfs-link FTP Server", nil
}

func (d *MainDriver) ClientDisconnected(cc ftpserver.ClientContext) {
	d.logger.Info("FTP client disconnected", "client_id", cc.ID(), "remote", cc.RemoteAddr().String())
}

func (d *MainDriver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	d.logger.Info("FTP login attempt", "client_id", cc.ID(), "user", user)
	if user != d.cfg.FTPUser || pass != d.cfg.FTPPass {
		return nil, errors.New("invalid username or password")
	}
	return vfs.New(d.store, d.objects), nil
}

func (d *MainDriver) GetTLSConfig() (*tls.Config, error) {
	return nil, errTLSNotConfigured
}
