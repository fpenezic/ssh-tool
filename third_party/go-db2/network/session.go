package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-db2/go-db2/converters"
	"github.com/go-db2/go-db2/internal/validate"
	"github.com/go-db2/go-db2/network/security"
	"github.com/go-db2/go-db2/types"
)

// SessionConfig holds configuration parameters for establishing a Db2 session.
type SessionConfig struct {
	Host              string
	Port              int
	Database          string
	User              string
	Password          string
	UseSSL            bool
	SSLRootCAPath     string
	SSLClientCertPath string
	SSLClientKeyPath  string
	TLSConfig         *tls.Config
	Timeout           time.Duration
	BlockSize         int
	SecurityMechanism uint16
	KerberosSPN       string
	KerberosRealm     string
	Krb5ConfigFile    string
	Krb5KeytabFile    string
	Krb5CCacheFile    string
	ClientApplName    string
	ClientWrkstnName  string
	ClientUserid      string
	ClientAcctng      string
	ClientCorrToken   string
}

func sanitizeLogString(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}

// String implements fmt.Stringer to ensure sensitive credentials (Password) are redacted in logs.
func (s SessionConfig) String() string {
	return fmt.Sprintf("SessionConfig{Host:%s, Port:%d, Database:%s, User:%s, Password:\"******\", UseSSL:%t, Timeout:%v}",
		sanitizeLogString(s.Host), s.Port, sanitizeLogString(s.Database), sanitizeLogString(s.User), s.UseSSL, s.Timeout)
}

// GoString implements fmt.GoStringer to ensure sensitive credentials (Password) are redacted in logs.
func (s SessionConfig) GoString() string {
	return s.String()
}

// ReplyPacket represents a single decoded DSS reply with its outer DDM codepoint and raw payload.
type ReplyPacket struct {
	Header    *DSSHeader
	CodePoint CodePoint
	Data      []byte
	MoreData  bool
}

// Session manages the underlying network socket and DRDA protocol state with the Db2 server.
type Session struct {
	cfg           SessionConfig
	conn          net.Conn
	mu            sync.Mutex
	correlationID uint16
	secmec        uint16
	encoding      StringEncoding
	endian        binary.ByteOrder
	prdid         string
	pkgid         string
	pkgcnstkn     string
	pkgsn         uint16
	qryblksz      uint32
	rdbinttkn     []byte
	autoCommit    bool
	closed        bool
	// broken is set once the byte stream can no longer be trusted: a socket
	// read or write failed, or a reply could not be framed. Unread bytes may
	// be left on the socket, so no further request may use it.
	broken atomic.Bool
}

// Broken reports whether the session has seen an I/O or framing error and
// must not be used for further requests.
func (s *Session) Broken() bool {
	return s.broken.Load()
}

// brokenOnErrorConn marks its session broken on the first failed read or write.
type brokenOnErrorConn struct {
	net.Conn
	broken *atomic.Bool
	// interruptedBy holds the context error that cut the current request short.
	interruptedBy atomic.Pointer[error]
}

func (c *brokenOnErrorConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		err = c.fail(err)
	}
	return n, err
}

func (c *brokenOnErrorConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		err = c.fail(err)
	}
	return n, err
}

// fail marks the session broken and, when the I/O was cut short by a context,
// reports the context's error so callers can match it with errors.Is.
func (c *brokenOnErrorConn) fail(err error) error {
	c.broken.Store(true)
	if cause := c.interruptedBy.Load(); cause != nil {
		return fmt.Errorf("%w (%v)", *cause, err)
	}
	return err
}

// watch interrupts the session's socket I/O when ctx is done, by moving the
// socket deadline to now; call the returned function once the request is over.
// The request is abandoned mid-exchange, so the session is marked broken and
// its connection is discarded.
func (s *Session) watch(ctx context.Context) func() {
	c, ok := s.conn.(*brokenOnErrorConn)
	if !ok || ctx.Done() == nil {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() {
		cause := context.Cause(ctx)
		c.interruptedBy.Store(&cause)
		c.broken.Store(true)
		_ = c.SetDeadline(time.Now())
	})
	return func() { stop() }
}

// NewSession instantiates a new Db2 network session configuration.
func NewSession(cfg SessionConfig) *Session {
	if cfg.Port == 0 {
		if cfg.UseSSL {
			cfg.Port = 50001
		} else {
			cfg.Port = 50000
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	dbPadded := fmt.Sprintf("%-18s", cfg.Database)
	if len(dbPadded) > 18 {
		dbPadded = dbPadded[:18]
	}
	cfg.Database = dbPadded

	blksz := uint32(cfg.BlockSize)
	if blksz < 1024 {
		blksz = 65535
	}

	secmec := cfg.SecurityMechanism
	if secmec == 0 {
		secmec = SecMecUSRIDPWD
	}

	return &Session{
		cfg:           cfg,
		correlationID: 1,
		secmec:        secmec,
		encoding:      EncodingCP500,
		endian:        binary.LittleEndian,
		prdid:         "SQL12010",
		pkgid:         "SYSSH200",
		pkgcnstkn:     "SYSLVL01",
		pkgsn:         65,
		qryblksz:      blksz,
		autoCommit:    true,
	}
}

// AutoCommit returns the current auto-commit state of the session.
func (s *Session) AutoCommit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCommit
}

// SetAutoCommit sets the auto-commit mode for subsequent operations.
func (s *Session) SetAutoCommit(auto bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoCommit = auto
}

// Connect dials the Db2 server via TCP/TLS and completes the DRDA handshake.
func (s *Session) Connect(ctx context.Context) error {
	if err := s.connectRaw(ctx); err != nil {
		return err
	}

	// Apply initial Client Info if configured in DSN (outside session mutex).
	if s.cfg.ClientApplName != "" || s.cfg.ClientWrkstnName != "" || s.cfg.ClientUserid != "" || s.cfg.ClientAcctng != "" || s.cfg.ClientCorrToken != "" {
		appl := s.cfg.ClientApplName
		wrkstn := s.cfg.ClientWrkstnName
		userid := s.cfg.ClientUserid
		acctng := s.cfg.ClientAcctng
		corr := s.cfg.ClientCorrToken

		// ResetClientInfo compares target registers against s.cfg in memory.
		// Clearing s.cfg client info fields ensures SetClientInfo executes the SET CLIENT commands on the Db2 server instead of skipping them thinking they were already set.
		s.mu.Lock()
		s.cfg.ClientApplName = ""
		s.cfg.ClientWrkstnName = ""
		s.cfg.ClientUserid = ""
		s.cfg.ClientAcctng = ""
		s.cfg.ClientCorrToken = ""
		s.mu.Unlock()

		if err := s.SetClientInfo(ctx, appl, wrkstn, userid, acctng, corr); err != nil {
			_ = s.Close()
			return fmt.Errorf("failed to apply client info (session closed): %w", err)
		}
	}

	return nil
}

func (s *Session) connectRaw(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		return errors.New("db2: session already connected")
	}

	address := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	dialer := &net.Dialer{
		Timeout: s.cfg.Timeout,
	}

	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to connect to Db2 at %s: %w", address, err)
	}

	if s.cfg.UseSSL {
		if s.cfg.TLSConfig != nil {
			// Clone so raising MinVersion never changes the caller's config.
			tlsConfig := s.cfg.TLSConfig.Clone()
			if tlsConfig.ServerName == "" {
				tlsConfig.ServerName = s.cfg.Host
			}
			if tlsConfig.MinVersion < tls.VersionTLS12 {
				tlsConfig.MinVersion = tls.VersionTLS12
			}
			tlsConn := tls.Client(rawConn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				_ = rawConn.Close()
				return fmt.Errorf("TLS handshake failed: %w", err)
			}
			rawConn = tlsConn
		} else {
			caPath := s.cfg.SSLRootCAPath
			if caPath == "" && s.cfg.SSLClientCertPath != "" && s.cfg.SSLClientKeyPath == "" {
				caPath = s.cfg.SSLClientCertPath
			}
			secTLSConfig := security.TLSConfig{
				EnableTLS:          true,
				InsecureSkipVerify: false,
				ServerName:         s.cfg.Host,
				CACertPath:         caPath,
				ClientCertPath:     s.cfg.SSLClientCertPath,
				ClientKeyPath:      s.cfg.SSLClientKeyPath,
			}
			tlsConn, tlsErr := security.WrapTLS(rawConn, secTLSConfig)
			if tlsErr != nil {
				_ = rawConn.Close()
				return fmt.Errorf("failed to establish TLS connection: %w", tlsErr)
			}
			rawConn = tlsConn
		}
	}

	if tcpConn, ok := rawConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}

	s.conn = &brokenOnErrorConn{Conn: rawConn, broken: &s.broken}
	s.closed = false
	defer s.watch(ctx)()

	// Perform full DRDA Handshake
	if err := s.handshake(ctx); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		s.closed = true
		return err
	}

	return nil
}

// handshake runs the DRDA initial protocol exchange (EXCSAT -> ACCSEC -> SECCHK -> ACCRDB).
func (s *Session) handshake(ctx context.Context) error {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "go-db2-client"
	}

	// 1. Pack EXCSAT
	excsat, err := PackEXCSAT("go-db2", hostname, "SQL12010", "go-db2", DefaultManagerLevels())
	if err != nil {
		return fmt.Errorf("failed to pack EXCSAT: %w", err)
	}

	// 2. Pack ACCSEC
	accsec, err := PackACCSEC(s.cfg.Database, s.secmec, nil)
	if err != nil {
		return fmt.Errorf("failed to pack ACCSEC: %w", err)
	}

	// Send EXCSAT (chained) and ACCSEC (last in chain)
	s.correlationID = 1
	s.correlationID, err = WriteRequestDSS(s.conn, excsat, s.correlationID, false, false)
	if err != nil {
		return fmt.Errorf("failed to send EXCSAT: %w", err)
	}
	s.correlationID, err = WriteRequestDSS(s.conn, accsec, s.correlationID, false, true)
	if err != nil {
		return fmt.Errorf("failed to send ACCSEC: %w", err)
	}

	// Read reply for ACCSEC
	negotiatedSecMec, secTkn, err := s.parseACCSECRD()
	if err != nil {
		return fmt.Errorf("ACCSEC negotiation failed: %w", err)
	}

	// If server wants another SECMEC, update and resend ACCSEC
	s.correlationID = 1
	if negotiatedSecMec != 0 && negotiatedSecMec != s.secmec {
		s.secmec = negotiatedSecMec
		accsecRetry, err := PackACCSEC(s.cfg.Database, s.secmec, nil)
		if err != nil {
			return err
		}
		s.correlationID, err = WriteRequestDSS(s.conn, accsecRetry, s.correlationID, false, false)
		if err != nil {
			return err
		}
	}

	// 3. Pack SECCHK (supporting Kerberos SECMEC 7/11, and SECMEC 9 with DH key exchange & DES encryption)
	var secchk []byte
	if s.secmec == security.SecMecKERBEROS || s.secmec == security.SecMecEUSERIDKERBEROS || s.secmec == 7 || s.secmec == 11 {
		krbCfg := security.KerberosConfig{
			ServicePrincipal: s.cfg.KerberosSPN,
			Host:             s.cfg.Host,
			Realm:            s.cfg.KerberosRealm,
			ConfigFile:       s.cfg.Krb5ConfigFile,
			KeytabFile:       s.cfg.Krb5KeytabFile,
			CCacheFile:       s.cfg.Krb5CCacheFile,
			Username:         s.cfg.User,
			Password:         s.cfg.Password,
		}
		krbToken, krbErr := security.AcquireKerberosToken(krbCfg)
		if krbErr != nil {
			return fmt.Errorf("failed to acquire Kerberos token: %w", krbErr)
		}
		secchk, err = PackSECCHKWithBytes(s.secmec, krbToken, s.cfg.Database, s.cfg.User, nil, false, s.encoding)
	} else if (s.secmec == SecMecEUSRIDPWD || s.secmec == 9) && len(secTkn) >= 20 {
		clientPriv, privErr := security.GenerateDHPrivateKey()
		if privErr != nil {
			return fmt.Errorf("failed to generate DH private key: %w", privErr)
		}
		clientPub := security.CalculateDHPublicKey(clientPriv)
		encPassword, encErr := security.EncryptPasswordSECMEC9(s.cfg.Password, secTkn, clientPriv)
		if encErr != nil {
			return fmt.Errorf("failed to encrypt password with SECMEC 9: %w", encErr)
		}
		secchk, err = PackSECCHKWithBytes(s.secmec, clientPub, s.cfg.Database, s.cfg.User, encPassword, true, s.encoding)
	} else {
		secchk, err = PackSECCHK(s.secmec, secTkn, s.cfg.Database, s.cfg.User, s.cfg.Password, s.encoding)
	}
	if err != nil {
		return fmt.Errorf("failed to pack SECCHK: %w", err)
	}

	// 4. Pack ACCRDB
	accrdb, err := PackACCRDB(s.prdid, s.cfg.Database, s.encoding)
	if err != nil {
		return fmt.Errorf("failed to pack ACCRDB: %w", err)
	}

	// Send SECCHK (chained) and ACCRDB (last in chain)
	s.correlationID, err = WriteRequestDSS(s.conn, secchk, s.correlationID, false, false)
	if err != nil {
		return fmt.Errorf("failed to send SECCHK: %w", err)
	}
	s.correlationID, err = WriteRequestDSS(s.conn, accrdb, s.correlationID, false, true)
	if err != nil {
		return fmt.Errorf("failed to send ACCRDB: %w", err)
	}

	// Parse reply chain for SECCHK and ACCRDB
	replies, err := s.readReplyChain()
	if err != nil {
		return fmt.Errorf("handshake response failed: %w", err)
	}

	// Validate reply status
	for _, r := range replies {
		if r.CodePoint == CodePointSECCHKRM {
			sub, _ := ParseDDMReply(r.Data)
			if secChkCd, ok := sub[CodePointSECCHKCD]; ok && len(secChkCd) > 0 && secChkCd[0] != 0 {
				return fmt.Errorf("authentication failed: SECCHKCD=%d", secChkCd[0])
			}
		}
		if r.CodePoint == CodePointACCRDBRM {
			sub, _ := ParseDDMReply(r.Data)
			if inttkn, ok := sub[CodePointRDBINTTKN]; ok {
				s.rdbinttkn = inttkn
			}
		}
		if r.CodePoint == CodePointRDBAFLRM || r.CodePoint == CodePointRDBATHRM {
			return errors.New("authorization failed connecting to relational database")
		}
		if r.CodePoint == CodePointSQLERRRM {
			sub, _ := ParseDDMReply(r.Data)
			srvDgn := string(sub[CodePointSRVDGN])
			return fmt.Errorf("server error during handshake: %s", srvDgn)
		}
	}

	// Post-connect setup: Configure UTF-8 CCSID and client environment
	if err := s.setPostConnectVariables(hostname); err != nil {
		return fmt.Errorf("failed to set post-connect variables: %w", err)
	}

	return nil
}

// setPostConnectVariables sends CCSID and locale initialization queries to Db2.
func (s *Session) setPostConnectVariables(hostname string) error {
	s.correlationID = 1

	// EXCSAT with CCSIDMGR = 1208 (UTF-8)
	excsatMgr := PackEXCSAT_MGRLVLLS([]ManagerLevel{{Manager: CodePointCCSIDMGR, Level: 1208}})
	var err error
	s.correlationID, err = WriteRequestDSS(s.conn, excsatMgr, s.correlationID, false, false)
	if err != nil {
		return err
	}

	targetWrkstn := hostname
	if s.cfg.ClientWrkstnName != "" {
		targetWrkstn = s.cfg.ClientWrkstnName
	}

	// EXCSQLSET
	excSqlSet := PackEXCSQLSET(s.pkgid, "", 1, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, excSqlSet, s.correlationID, true, false)
	if err != nil {
		return err
	}

	// SET CLIENT WRKSTNNAME
	sqlWrkStn := PackSQLSTT(fmt.Sprintf("SET CLIENT WRKSTNNAME '%s'", escapeSingleQuotes(targetWrkstn)))
	s.correlationID, err = WriteRequestDSS(s.conn, sqlWrkStn, s.correlationID, false, false)
	if err != nil {
		return err
	}

	// RDBCMM (Commit setup)
	rdbcmm := PackRDBCMM()
	s.correlationID, err = WriteRequestDSS(s.conn, rdbcmm, s.correlationID, false, true)
	if err != nil {
		return err
	}

	// Drain responses
	_, err = s.readReplyChain()
	return err
}

// parseACCSECRD reads and processes the ACCSECRD reply during security negotiation.
func (s *Session) parseACCSECRD() (uint16, []byte, error) {
	replies, err := s.readReplyChain()
	if err != nil {
		return 0, nil, err
	}

	var secmec uint16
	var sectkn []byte

	for _, r := range replies {
		if r.CodePoint == CodePointRDBNFNRM {
			return 0, nil, errors.New("relational database not found (RDBNFNRM)")
		}
		if r.CodePoint == CodePointACCSECRD {
			sub, err := ParseDDMReply(r.Data)
			if err != nil {
				return 0, nil, err
			}
			if smBytes, ok := sub[CodePointSECMEC]; ok && len(smBytes) >= 2 {
				secmec = binary.BigEndian.Uint16(smBytes[:2])
			}
			if stBytes, ok := sub[CodePointSECTKN]; ok {
				sectkn = stBytes
			}
		}
	}

	return secmec, sectkn, nil
}

// readReplyChain reads all chained DSS reply packets until the end of the chain.
func (s *Session) readReplyChain() ([]ReplyPacket, error) {
	var packets []ReplyPacket
	chained := true

	for chained {
		hdr, cp, data, moreData, err := ReadDSS(s.conn)
		if err != nil {
			// A frame that failed to parse may be only partly consumed.
			s.broken.Store(true)
			return nil, err
		}
		packets = append(packets, ReplyPacket{
			Header:    hdr,
			CodePoint: cp,
			Data:      data,
			MoreData:  moreData,
		})
		chained = hdr.Chained
	}

	return packets, nil
}

// Ping checks if the database session is responsive.
func (s *Session) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	// RDBCMM as a lightweight ping
	s.correlationID = 1
	rdbcmm := PackRDBCMM()
	var err error
	s.correlationID, err = WriteRequestDSS(s.conn, rdbcmm, s.correlationID, false, true)
	_, err = s.readReplyChain()
	return err
}

// Commit commits the active transaction.
func (s *Session) Commit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	rdbcmm := PackRDBCMM()
	var err error
	s.correlationID, err = WriteRequestDSS(s.conn, rdbcmm, s.correlationID, false, true)
	if err != nil {
		return err
	}

	_, err = s.readReplyChain()
	return err
}

// Rollback rolls back the active transaction.
func (s *Session) Rollback(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	rdbrllbck := PackRDBRLLBCK()
	var err error
	s.correlationID, err = WriteRequestDSS(s.conn, rdbrllbck, s.correlationID, false, true)
	if err != nil {
		return err
	}

	_, err = s.readReplyChain()
	return err
}

// ExecDirect executes a DDL or DML statement immediately without parameter binding.
func (s *Session) ExecDirect(ctx context.Context, sql string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return 0, errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	var err error

	// 1. EXCSQLIMM (Execute Immediate)
	imm := PackEXCSQLIMM(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, imm, s.correlationID, true, false)
	if err != nil {
		return 0, fmt.Errorf("failed to write EXCSQLIMM: %w", err)
	}

	// 2. SQLSTT (SQL Statement Text)
	stt := PackSQLSTT(sql)
	if s.autoCommit {
		s.correlationID, err = WriteRequestDSS(s.conn, stt, s.correlationID, false, false)
		if err != nil {
			return 0, fmt.Errorf("failed to write SQLSTT: %w", err)
		}

		// 3. RDBCMM (Commit)
		cmm := PackRDBCMM()
		s.correlationID, err = WriteRequestDSS(s.conn, cmm, s.correlationID, false, true)
		if err != nil {
			return 0, fmt.Errorf("failed to write RDBCMM: %w", err)
		}
	} else {
		s.correlationID, err = WriteRequestDSS(s.conn, stt, s.correlationID, false, true)
		if err != nil {
			return 0, fmt.Errorf("failed to write SQLSTT: %w", err)
		}
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	var affectedRows int64
	for _, r := range replies {
		if r.CodePoint == CodePointSQLCARD {
			code, state, msg, rows, parseErr := ParseSQLCARD(r.Data, s.endian)
			if parseErr != nil {
				return 0, parseErr
			}
			if code < 0 {
				return 0, fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s %s", code, state, msg)
			}
			affectedRows += rows
		}
		if r.CodePoint == CodePointSQLERRRM {
			sub, _ := ParseDDMReply(r.Data)
			return 0, fmt.Errorf("db2: %s", string(sub[CodePointSRVDGN]))
		}
	}

	return affectedRows, nil
}

// QueryDirect prepares, describes and executes a query statement returning raw column headers and decoded row data.
// Optimization: Returns decoded rows directly as [][]driver.Value to avoid per-row slice re-allocation and copying in Conn/Stmt wrappers.
func (s *Session) QueryDirect(ctx context.Context, sql string) ([]ColumnDescription, [][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return nil, nil, errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	var err error

	// 1. PRPSQLSTT
	prp := PackPRPSQLSTT(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, prp, s.correlationID, true, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write PRPSQLSTT: %w", err)
	}

	// 2. SQLSTT
	stt := PackSQLSTT(sql)
	s.correlationID, err = WriteRequestDSS(s.conn, stt, s.correlationID, false, false)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write SQLSTT: %w", err)
	}

	// 3. OPNQRY
	opn := PackOPNQRY(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database, s.qryblksz)
	s.correlationID, err = WriteRequestDSS(s.conn, opn, s.correlationID, false, true)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write OPNQRY: %w", err)
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read query response: %w", err)
	}

	return s.fetchQuery(replies, nil)
}

// PrepareAndDescribe prepares a query/statement and retrieves its output columns and input parameter descriptors.
func (s *Session) PrepareAndDescribe(ctx context.Context, sql string) ([]ColumnDescription, []ColumnDescription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return nil, nil, errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	var err error

	// 1. PRPSQLSTT
	prp := PackPRPSQLSTT(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, prp, s.correlationID, true, false)
	if err != nil {
		return nil, nil, err
	}

	// 2. SQLSTT
	stt := PackSQLSTT(sql)
	s.correlationID, err = WriteRequestDSS(s.conn, stt, s.correlationID, false, false)
	if err != nil {
		return nil, nil, err
	}

	// 3. DSCSQLSTT
	dsc := PackDSCSQLSTT(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, dsc, s.correlationID, false, true)
	if err != nil {
		return nil, nil, err
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return nil, nil, err
	}

	var outputCols, paramCols []ColumnDescription
	for _, r := range replies {
		if r.CodePoint == CodePointSQLDARD {
			cols, err := ParseSQLDARD(r.Data, s.endian)
			if err != nil {
				return nil, nil, err
			}
			if len(r.Data) > 0 && r.Data[0] == 0xFF {
				paramCols = cols
			} else {
				outputCols = cols
			}
		} else if r.CodePoint == CodePointSQLCARD {
			code, state, msg, _, parseErr := ParseSQLCARD(r.Data, s.endian)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			if code < 0 {
				return nil, nil, fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s %s", code, state, msg)
			}
		} else if r.CodePoint == CodePointSQLERRRM {
			sub, _ := ParseDDMReply(r.Data)
			return nil, nil, fmt.Errorf("db2: %s", string(sub[CodePointSRVDGN]))
		}
	}

	return outputCols, paramCols, nil
}

// ExecWithParams executes a prepared statement with parameter arguments and returns affected rows and any output parameters.
func (s *Session) ExecWithParams(ctx context.Context, paramCols []ColumnDescription, args []any) (int64, []any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return 0, nil, errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	colTypes := make([]types.SQLType, len(paramCols))
	colLens := make([]int64, len(paramCols))
	precs := make([]int, len(paramCols))
	scales := make([]int, len(paramCols))

	for i, c := range paramCols {
		colTypes[i] = types.SQLType(c.SQLType)
		colLens[i] = c.Length
		precs[i] = c.Precision
		scales[i] = c.Scale
	}

	sqldta, err := converters.BuildSQLDTA(colTypes, colLens, precs, scales, args, s.endian)
	if err != nil {
		return 0, nil, err
	}

	// 1. EXCSQLSTT
	exc := PackEXCSQLSTT(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	curID, err := WriteRequestDSS(s.conn, exc, 1, true, false)
	if err != nil {
		return 0, nil, err
	}

	// 2. SQLDTA
	if s.autoCommit {
		_, err = WriteRequestDSS(s.conn, sqldta, curID, false, false)
		if err != nil {
			return 0, nil, err
		}

		// 3. RDBCMM
		cmm := PackRDBCMM()
		_, err = WriteRequestDSS(s.conn, cmm, 1, false, true)
		if err != nil {
			return 0, nil, err
		}
	} else {
		_, err = WriteRequestDSS(s.conn, sqldta, curID, false, true)
		if err != nil {
			return 0, nil, err
		}
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return 0, nil, err
	}

	// If the server sent SQLDARD in the first chain, a second chain follows with SQLDTARD / SQLCARD
	hasOnlyDARD := false
	for _, r := range replies {
		if r.CodePoint == CodePointSQLDARD {
			hasOnlyDARD = true
			break
		}
	}
	if hasOnlyDARD {
		replies2, err := s.readReplyChain()
		if err == nil {
			replies = append(replies, replies2...)
		}
	}

	var affectedRows int64
	var outValues []any
	for _, r := range replies {
		if r.CodePoint == CodePointSQLDTARD {
			vals, parseErr := converters.ParseSQLDTARD(r.Data, s.endian)
			if parseErr == nil && len(vals) > 0 {
				outValues = vals
			}
		} else if r.CodePoint == CodePointSQLCARD {
			code, state, msg, rows, parseErr := ParseSQLCARD(r.Data, s.endian)
			if parseErr != nil {
				return 0, nil, parseErr
			}
			if code < 0 {
				return 0, nil, fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s %s", code, state, msg)
			}
			affectedRows += rows
		} else if r.CodePoint == CodePointSQLERRRM {
			sub, _ := ParseDDMReply(r.Data)
			return 0, nil, fmt.Errorf("db2: %s", string(sub[CodePointSRVDGN]))
		}
	}

	return affectedRows, outValues, nil
}

// ExecBatchWithParams executes a batch of parameter sets against the currently prepared statement.
func (s *Session) ExecBatchWithParams(ctx context.Context, paramCols []ColumnDescription, batchRows [][]any) (int64, error) {
	if len(batchRows) == 0 {
		return 0, nil
	}

	var totalAffected int64
	for _, rowArgs := range batchRows {
		affected, _, err := s.ExecWithParams(ctx, paramCols, rowArgs)
		if err != nil {
			return totalAffected, err
		}
		totalAffected += affected
	}
	return totalAffected, nil
}

// QueryWithParams opens a query on a prepared statement with parameter arguments.
// Optimization: Returns decoded rows directly as [][]driver.Value to avoid per-row slice re-allocation and copying in Conn/Stmt wrappers.
func (s *Session) QueryWithParams(ctx context.Context, outputCols, paramCols []ColumnDescription, args []any) ([]ColumnDescription, [][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return nil, nil, errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	colTypes := make([]types.SQLType, len(paramCols))
	colLens := make([]int64, len(paramCols))
	precs := make([]int, len(paramCols))
	scales := make([]int, len(paramCols))

	for i, c := range paramCols {
		colTypes[i] = types.SQLType(c.SQLType)
		colLens[i] = c.Length
		precs[i] = c.Precision
		scales[i] = c.Scale
	}

	sqldta, err := converters.BuildSQLDTA(colTypes, colLens, precs, scales, args, s.endian)
	if err != nil {
		return nil, nil, err
	}

	// 1. OPNQRY with QRYBLKSZ
	opnqry := PackOPNQRY(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database, s.qryblksz)
	curID, err := WriteRequestDSS(s.conn, opnqry, 1, true, false)
	if err != nil {
		return nil, nil, err
	}

	// 2. SQLDTA with parameters
	_, err = WriteRequestDSS(s.conn, sqldta, curID, false, true)
	if err != nil {
		return nil, nil, err
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return nil, nil, err
	}

	columns, rows, err := s.fetchQuery(replies, outputCols)
	if err != nil {
		return nil, nil, err
	}

	if s.autoCommit {
		// Send commit to close query lock
		s.correlationID = 1
		s.correlationID, err = WriteRequestDSS(s.conn, PackRDBCMM(), s.correlationID, false, true)
		if err == nil {
			_, _ = s.readReplyChain()
		}
	}

	return columns, rows, nil
}

// queryResult accumulates the replies to one query: its descriptors, the row
// data of every query block and the externalized (EXTDTA) values.
type queryResult struct {
	columns []ColumnDescription
	fields  []FieldDescriptor
	// rowData is the QRYDTA of every block, concatenated: a row may start in
	// one block and end in the next.
	rowData  bytes.Buffer
	extdta   [][]byte
	open     bool // OPNQRYRM seen: the cursor is open on the server
	ended    bool // ENDQRYRM or SQLCODE +100 seen: the server closed it
	qryinsid uint64
	cntqryID uint16
}

// consume records one reply chain. It reports whether the chain carried any
// query data, and returns the first error the server reported in it.
func (q *queryResult) consume(replies []ReplyPacket, endian binary.ByteOrder) (bool, error) {
	progress := false
	for _, r := range replies {
		switch r.CodePoint {
		case CodePointSQLDARD:
			cols, err := ParseSQLDARD(r.Data, endian)
			if err != nil {
				return progress, err
			}
			if len(cols) > 0 {
				q.columns = cols
			}
		case CodePointQRYDSC:
			flds, err := ParseQRYDSC(r.Data)
			if err != nil {
				return progress, err
			}
			q.fields = flds
		case CodePointQRYDTA:
			q.rowData.Write(r.Data)
			progress = true
		case CodePointEXTDTA:
			q.extdta = append(q.extdta, r.Data)
			progress = true
		case CodePointOPNQRYRM:
			sub, _ := ParseDDMReply(r.Data)
			if insidBytes, ok := sub[CodePointQRYINSID]; ok && len(insidBytes) >= 8 {
				q.qryinsid = binary.BigEndian.Uint64(insidBytes)
			}
			q.open = true
			q.cntqryID = r.Header.CorrelationID
		case CodePointENDQRYRM:
			q.ended = true
		case CodePointSQLERRRM:
			sub, _ := ParseDDMReply(r.Data)
			return progress, fmt.Errorf("db2: %s", string(sub[CodePointSRVDGN]))
		case CodePointSQLCARD:
			code, state, msg, _, err := ParseSQLCARD(r.Data, endian)
			if err != nil {
				return progress, err
			}
			if code < 0 {
				return progress, fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s %s", code, state, msg)
			}
			if code == 100 {
				q.ended = true
			}
		}
	}
	return progress, nil
}

// fetchQuery reads a query's result from the replies to OPNQRY, continuing
// the query with CNTQRY, one query block at a time, until the server ends it.
func (s *Session) fetchQuery(replies []ReplyPacket, columns []ColumnDescription) ([]ColumnDescription, [][]driver.Value, error) {
	q := &queryResult{columns: columns, cntqryID: 1}
	if _, err := q.consume(replies, s.endian); err != nil {
		return nil, nil, err
	}

	for q.open && !q.ended {
		cntqry := PackCNTQRY(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database, s.qryblksz, q.qryinsid)
		if _, err := WriteRequestDSS(s.conn, cntqry, q.cntqryID, false, true); err != nil {
			return nil, nil, fmt.Errorf("failed to write CNTQRY: %w", err)
		}
		cntReplies, err := s.readReplyChain()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read CNTQRY response: %w", err)
		}
		progress, err := q.consume(cntReplies, s.endian)
		if err != nil {
			return nil, nil, err
		}
		if !progress && !q.ended {
			return nil, nil, errors.New("db2: CNTQRY returned no query data and did not end the query")
		}
	}

	rows, err := decodeRows(q.fields, q.rowData.Bytes(), s.endian)
	if err != nil {
		return nil, nil, err
	}
	if len(q.extdta) > 0 {
		stitchEXTDTA(q.fields, rows, q.extdta)
	}
	return q.columns, rows, nil
}

// decodeRows decodes QRYDTA row data. Each row starts with its SQLCA, which is
// the null indicator 0xFF unless the server attached a warning or an error to
// the row, then the indicator of the row data (0x00 when present), then one
// value per field.
// Optimization: Decodes rows directly as driver.Value slices and allocates a single contiguous backing slice for row buffers to eliminate per-row heap slice allocations.
func decodeRows(fields []FieldDescriptor, data []byte, endian binary.ByteOrder) ([][]driver.Value, error) {
	reader := bytes.NewReader(data)
	numFields := len(fields)

	// Pre-allocate rows capacity based on data length and field count to minimize slice reallocations.
	initCap := 0
	if minRowLen := 2 + numFields; minRowLen > 0 && len(data) >= minRowLen {
		initCap = len(data) / minRowLen
		if initCap > 1024 {
			initCap = 1024
		}
	}
	rows := make([][]driver.Value, 0, initCap)

	// Optimization: Allocate a single contiguous backing slice for all row buffers when field count and initial capacity are known.
	// Sub-slicing row buffers from backing eliminates N individual row slice heap allocations per query result set.
	var backing []driver.Value
	if initCap > 0 && numFields > 0 {
		backing = make([]driver.Value, initCap*numFields)
	}

	for reader.Len() >= 2 {
		first, err := reader.ReadByte()
		if err != nil {
			break
		}
		if first == 0xFF {
			// Fast-path: Null SQLCA indicator (normal row without server warnings or errors)
			dataInd, err := reader.ReadByte()
			if err != nil || dataInd != 0x00 {
				break
			}
		} else if first == 0x00 {
			// Non-null SQLCA indicator present (row has attached warning or error)
			_ = reader.UnreadByte()
			code, state, err := readRowSQLCA(reader, endian)
			if err != nil {
				return nil, fmt.Errorf("failed to read the SQLCA of row %d: %w", len(rows)+1, err)
			}
			if code < 0 {
				return nil, fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s in row %d", code, state, len(rows)+1)
			}
			dataInd, err := reader.ReadByte()
			if err != nil || dataInd != 0x00 {
				break
			}
		} else {
			break
		}

		var row []driver.Value
		if len(backing) >= numFields {
			row, backing = backing[:numFields:numFields], backing[numFields:]
		} else {
			row = make([]driver.Value, numFields)
		}

		for i := 0; i < numFields; i++ {
			f := fields[i]
			val, err := converters.DecodeField(f.Type, f.PS[:], reader, endian)
			if err != nil {
				return nil, fmt.Errorf("failed to decode row %d column %d (Type=0x%02X, PS=%x, RemainingBytes=%d): %w",
					len(rows)+1, i+1, f.Type, f.PS[:], reader.Len(), err)
			}
			row[i] = val
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// readRowSQLCA reads the SQLCA group a QRYDTA row starts with and returns its
// SQLCODE and SQLSTATE; a null SQLCA (0xFF) is SQLCODE 0. The layout is the
// SQLCAGRP of DRDA: SQLCODE, SQLSTATE, SQLERRPROC, then the SQLCAXGRP (SQLERRD,
// SQLWARN, the RDB name and two message texts) and the SQLDIAGGRP, each behind
// its own null indicator.
func readRowSQLCA(r *bytes.Reader, endian binary.ByteOrder) (int32, string, error) {
	ind, err := r.ReadByte()
	if err != nil {
		return 0, "", err
	}
	if ind == 0xFF {
		return 0, "", nil
	}
	var head [4 + 5 + 8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, "", err
	}
	code := int32(endian.Uint32(head[0:4]))
	state := string(head[4:9])

	xInd, err := r.ReadByte()
	if err != nil {
		return 0, "", err
	}
	if xInd != 0xFF {
		if _, err := r.Seek(24+11, io.SeekCurrent); err != nil { // SQLERRD1-6, SQLWARN0-A
			return 0, "", err
		}
		for range 3 { // SQLRDBNAME, SQLERRMSG_m, SQLERRMSG_s
			var ln [2]byte
			if _, err := io.ReadFull(r, ln[:]); err != nil {
				return 0, "", err
			}
			if _, err := r.Seek(int64(binary.BigEndian.Uint16(ln[:])), io.SeekCurrent); err != nil {
				return 0, "", err
			}
		}
	}
	diagInd, err := r.ReadByte()
	if err != nil {
		return 0, "", err
	}
	if diagInd != 0xFF {
		return 0, "", fmt.Errorf("SQLCODE=%d SQLSTATE=%s carries an SQLDIAGGRP, which is not supported", code, state)
	}
	return code, state, nil
}

func isLOBType(t uint8) bool {
	switch t {
	case converters.DRDATypeLOBLOC, converters.DRDATypeNLOBLOC,
		converters.DRDATypeCLOBLOC, converters.DRDATypeNCLOBLOC,
		converters.DRDATypeDBCSCLOBLOC, converters.DRDATypeNDBCSCLOBLOC,
		converters.DRDATypeLOBBytes, converters.DRDATypeNLOBBytes,
		converters.DRDATypeLOBCSBCS, converters.DRDATypeNLOBCSBCS,
		converters.DRDATypeXML, converters.DRDATypeNXML,
		0x10, 0x11, 0xCD, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8, 0xF9:
		return true
	default:
		return false
	}
}

func isCLOBType(t uint8) bool {
	switch t {
	case converters.DRDATypeCLOBLOC, converters.DRDATypeNCLOBLOC,
		converters.DRDATypeDBCSCLOBLOC, converters.DRDATypeNDBCSCLOBLOC,
		converters.DRDATypeLOBCSBCS, converters.DRDATypeNLOBCSBCS,
		converters.DRDATypeXML, converters.DRDATypeNXML,
		0xCD, 0xF6, 0xF7, 0xF8, 0xF9:
		return true
	default:
		return false
	}
}

func isDBCLOBType(t uint8) bool {
	switch t {
	case converters.DRDATypeDBCSCLOBLOC, converters.DRDATypeNDBCSCLOBLOC,
		0xCD, 0xF8, 0xF9:
		return true
	default:
		return false
	}
}

func stitchEXTDTA(fields []FieldDescriptor, rows [][]driver.Value, extdtaList [][]byte) {
	var lobIndices []int
	for i, f := range fields {
		if isLOBType(f.Type) {
			lobIndices = append(lobIndices, i)
		}
	}
	if len(lobIndices) == 0 {
		return
	}

	extIdx := 0
	for rowIdx := range rows {
		for _, colIdx := range lobIndices {
			if rows[rowIdx][colIdx] != nil && extIdx < len(extdtaList) {
				data := extdtaList[extIdx]
				// EXTDTA data starts with a 1-byte status flag (0x00 = valid data)
				if len(data) > 0 && data[0] == 0x00 {
					data = data[1:]
				}
				fType := fields[colIdx].Type
				if isDBCLOBType(fType) {
					rows[rowIdx][colIdx] = converters.DecodeUTF16BE(data)
				} else if isCLOBType(fType) {
					rows[rowIdx][colIdx] = string(data)
				} else {
					rows[rowIdx][colIdx] = data
				}
				extIdx++
			}
		}
	}
}

// CurrentUser returns the currently active user on the session.
func (s *Session) CurrentUser() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.cfg.User)
}

// quoteIdentifier safely quotes an SQL identifier with double quotes, escaping any embedded double quotes.
// Optimization: Checks for double quotes with strings.IndexByte fast-path and pre-allocates exact capacity in strings.Builder to eliminate unnecessary allocations and string scanning overhead.
func quoteIdentifier(name string) string {
	if strings.IndexByte(name, '"') == -1 {
		return `"` + name + `"`
	}
	count := strings.Count(name, `"`)
	var sb strings.Builder
	sb.Grow(len(name) + 2 + count)
	sb.WriteByte('"')
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch == '"' {
			sb.WriteString(`""`)
		} else {
			sb.WriteByte(ch)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// SwitchUser transitions the active user identity on the existing session.
// In Db2 Trusted Context and privileged sessions, identity is switched via
// 'SET SESSION AUTHORIZATION = ?' / 'SET SESSION_USER = ?' or by issuing SECCHK with USRID.
func (s *Session) SwitchUser(ctx context.Context, newUser string, password ...string) error {
	trimmedUser := strings.TrimSpace(newUser)
	if trimmedUser == "" {
		return errors.New("db2: switch user name cannot be empty")
	}
	if !validate.IsValidSQLIdentifier(trimmedUser) {
		return fmt.Errorf("db2: invalid switch user identifier %q", newUser)
	}

	quotedUser := quoteIdentifier(trimmedUser)

	// First try SQL-level session authorization switch
	setAuthSQL := fmt.Sprintf("SET SESSION AUTHORIZATION = %s", quotedUser)
	if _, err := s.ExecDirect(ctx, setAuthSQL); err == nil {
		s.mu.Lock()
		s.cfg.User = trimmedUser
		s.mu.Unlock()
		return nil
	}

	// Fallback to SET SESSION_USER
	setSessionUserSQL := fmt.Sprintf("SET SESSION_USER = %s", quotedUser)
	if _, err := s.ExecDirect(ctx, setSessionUserSQL); err == nil {
		s.mu.Lock()
		s.cfg.User = trimmedUser
		s.mu.Unlock()
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	// If password or DRDA-level SECCHK user switch is needed
	var pwdBytes []byte
	if len(password) > 0 && password[0] != "" {
		pwdBytes = []byte(password[0])
	}
	secchk, err := PackSECCHKWithBytes(s.secmec, nil, s.cfg.Database, trimmedUser, pwdBytes, false, s.encoding)
	if err != nil {
		return fmt.Errorf("failed to pack SECCHK for user switch: %w", err)
	}

	s.correlationID = 1
	s.correlationID, err = WriteRequestDSS(s.conn, secchk, s.correlationID, false, true)
	if err != nil {
		return fmt.Errorf("failed to send SECCHK for user switch: %w", err)
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return fmt.Errorf("user switch response failed: %w", err)
	}

	for _, r := range replies {
		if r.CodePoint == CodePointSECCHKRM {
			sub, _ := ParseDDMReply(r.Data)
			if secChkCd, ok := sub[CodePointSECCHKCD]; ok && len(secChkCd) > 0 && secChkCd[0] != 0 {
				return fmt.Errorf("user switch failed: SECCHKCD=%d", secChkCd[0])
			}
		}
	}

	s.cfg.User = trimmedUser
	return nil
}

// ExecSQLSet executes special register statements (such as SET CLIENT APPLNAME) via DRDA EXCSQLSET.
func (s *Session) ExecSQLSet(ctx context.Context, sql string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}
	defer s.watch(ctx)()

	s.correlationID = 1
	var err error

	excSqlSet := PackEXCSQLSET(s.pkgid, "", 1, s.cfg.Database)
	s.correlationID, err = WriteRequestDSS(s.conn, excSqlSet, s.correlationID, true, false)
	if err != nil {
		return fmt.Errorf("failed to write EXCSQLSET: %w", err)
	}

	sqlStt := PackSQLSTT(sql)
	if s.autoCommit {
		s.correlationID, err = WriteRequestDSS(s.conn, sqlStt, s.correlationID, false, false)
		if err != nil {
			return fmt.Errorf("failed to write SQLSTT: %w", err)
		}

		rdbcmm := PackRDBCMM()
		s.correlationID, err = WriteRequestDSS(s.conn, rdbcmm, s.correlationID, false, true)
		if err != nil {
			return fmt.Errorf("failed to write RDBCMM: %w", err)
		}
	} else {
		s.correlationID, err = WriteRequestDSS(s.conn, sqlStt, s.correlationID, false, true)
		if err != nil {
			return fmt.Errorf("failed to write SQLSTT: %w", err)
		}
	}

	replies, err := s.readReplyChain()
	if err != nil {
		return fmt.Errorf("failed to read reply chain for EXCSQLSET: %w", err)
	}

	for _, r := range replies {
		if r.CodePoint == CodePointSQLCARD {
			code, state, msg, _, parseErr := ParseSQLCARD(r.Data, s.endian)
			if parseErr != nil {
				return parseErr
			}
			if code < 0 {
				return fmt.Errorf("db2: SQLCODE=%d SQLSTATE=%s %s", code, state, msg)
			}
		}
	}

	return nil
}

// ClientInfoMatches returns true if the current session registers match the given values.
func (s *Session) ClientInfoMatches(applName, wrkstnName, userid, acctng, corrToken string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.ClientApplName == applName &&
		s.cfg.ClientWrkstnName == wrkstnName &&
		s.cfg.ClientUserid == userid &&
		s.cfg.ClientAcctng == acctng &&
		s.cfg.ClientCorrToken == corrToken
}

// ResetClientInfo restores all client information special registers back to the given base configuration.
func (s *Session) ResetClientInfo(ctx context.Context, applName, wrkstnName, userid, acctng, corrToken string) error {
	applName = sanitizeClientRegister(applName)
	wrkstnName = sanitizeClientRegister(wrkstnName)
	userid = sanitizeClientRegister(userid)
	acctng = sanitizeClientRegister(acctng)
	corrToken = sanitizeClientRegister(corrToken)

	s.mu.Lock()
	prevAppl := s.cfg.ClientApplName
	prevWrkstn := s.cfg.ClientWrkstnName
	prevUserid := s.cfg.ClientUserid
	prevAcctng := s.cfg.ClientAcctng
	prevCorr := s.cfg.ClientCorrToken
	s.mu.Unlock()

	if applName != prevAppl {
		if err := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT APPLNAME '%s'", escapeSingleQuotes(applName))); err != nil {
			return err
		}
	}
	if wrkstnName != prevWrkstn {
		if err := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT WRKSTNNAME '%s'", escapeSingleQuotes(wrkstnName))); err != nil {
			return err
		}
	}
	if userid != prevUserid {
		if err := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT USERID '%s'", escapeSingleQuotes(userid))); err != nil {
			return err
		}
	}
	if acctng != prevAcctng {
		if err := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT ACCTNG '%s'", escapeSingleQuotes(acctng))); err != nil {
			if err2 := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT_ACCTNG = '%s'", escapeSingleQuotes(acctng))); err2 != nil {
				if err3 := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT ACCTSTR '%s'", escapeSingleQuotes(acctng))); err3 != nil {
					return err3
				}
			}
		}
	}
	if corrToken != prevCorr {
		if err := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT CORR_TOKEN '%s'", escapeSingleQuotes(corrToken))); err != nil {
			if err2 := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT_CORR_TOKEN = '%s'", escapeSingleQuotes(corrToken))); err2 != nil {
				if err3 := s.ExecSQLSet(ctx, fmt.Sprintf("SET CLIENT PROGRAMID '%s'", escapeSingleQuotes(corrToken))); err3 != nil {
					return err3
				}
			}
		}
	}

	s.mu.Lock()
	s.cfg.ClientApplName = applName
	s.cfg.ClientWrkstnName = wrkstnName
	s.cfg.ClientUserid = userid
	s.cfg.ClientAcctng = acctng
	s.cfg.ClientCorrToken = corrToken
	s.mu.Unlock()

	return nil
}

// SetClientInfo updates specified client information registers on the session. Empty fields in args leave existing registers unchanged.
func (s *Session) SetClientInfo(ctx context.Context, applName, wrkstnName, userid, acctng, corrToken string) error {
	s.mu.Lock()
	targetAppl := s.cfg.ClientApplName
	targetWrkstn := s.cfg.ClientWrkstnName
	targetUserid := s.cfg.ClientUserid
	targetAcctng := s.cfg.ClientAcctng
	targetCorr := s.cfg.ClientCorrToken
	s.mu.Unlock()

	if applName != "" {
		targetAppl = applName
	}
	if wrkstnName != "" {
		targetWrkstn = wrkstnName
	}
	if userid != "" {
		targetUserid = userid
	}
	if acctng != "" {
		targetAcctng = acctng
	}
	if corrToken != "" {
		targetCorr = corrToken
	}

	return s.ResetClientInfo(ctx, targetAppl, targetWrkstn, targetUserid, targetAcctng, targetCorr)
}

func sanitizeClientRegister(val string) string {
	idx := strings.IndexAny(val, "\x00\r\n")
	if idx == -1 {
		return val
	}
	var sb strings.Builder
	sb.Grow(len(val))
	sb.WriteString(val[:idx])
	for i := idx; i < len(val); i++ {
		ch := val[i]
		if ch != '\x00' && ch != '\r' && ch != '\n' {
			sb.WriteByte(ch)
		}
	}
	return sb.String()
}

// escapeSingleQuotes sanitizes and escapes single quote characters for SQL literal strings.
// Optimization: Fast-path using strings.IndexAny returns clean strings immediately (0 allocs) and uses a single pre-allocated strings.Builder pass for escaping/sanitizing modified strings.
func escapeSingleQuotes(val string) string {
	idx := strings.IndexAny(val, "\x00\r\n'")
	if idx == -1 {
		return val
	}
	var sb strings.Builder
	sb.Grow(len(val) + 8)
	sb.WriteString(val[:idx])
	for i := idx; i < len(val); i++ {
		ch := val[i]
		switch ch {
		case '\x00', '\r', '\n':
			// skip control characters
		case '\'':
			sb.WriteString("''")
		default:
			sb.WriteByte(ch)
		}
	}
	return sb.String()
}

// Interrupt requests cancellation of any active statement on the session via SQLINTR.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
		return errors.New("db2: connection is closed")
	}

	intr := PackSQLINTR(s.pkgid, s.pkgcnstkn, s.pkgsn, s.cfg.Database)
	_, err := WriteRequestDSS(s.conn, intr, 1, false, true)
	return err
}

// Close gracefully closes the session and underlying network connection.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}
