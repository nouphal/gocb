package gocb

import (
	"crypto/x509"
	"fmt"
	"strconv"
	"sync"
	"time"

	gocbcore "github.com/couchbase/gocbcore/v9"
	gocbconnstr "github.com/couchbase/gocbcore/v9/connstr"
	"github.com/pkg/errors"
)

// Cluster represents a connection to a specific Couchbase cluster.
type Cluster struct {
	cSpec gocbconnstr.ConnSpec
	auth  Authenticator

	connectionsLock sync.RWMutex
	connections     map[string]client
	clusterClient   client

	clusterLock sync.RWMutex
	queryCache  map[string]*queryCacheEntry

	sb stateBlock

	supportsEnhancedStatements int32

	supportsGCCCP bool
}

// IoConfig specifies IO related configuration options.
type IoConfig struct {
	DisableMutationTokens  bool
	DisableServerDurations bool
}

// TimeoutsConfig specifies options for various operation timeouts.
type TimeoutsConfig struct {
	ConnectTimeout time.Duration
	KVTimeout      time.Duration
	// Volatile: This option is subject to change at any time.
	KVDurableTimeout  time.Duration
	ViewTimeout       time.Duration
	QueryTimeout      time.Duration
	AnalyticsTimeout  time.Duration
	SearchTimeout     time.Duration
	ManagementTimeout time.Duration
}

// OrphanReporterConfig specifies options for controlling the orphan
// reporter which records when the SDK receives responses for requests
// that are no longer in the system (usually due to being timed out).
type OrphanReporterConfig struct {
	Disabled       bool
	ReportInterval time.Duration
	SampleSize     uint32
}

// SecurityConfig specifies options for controlling security related
// items such as TLS root certificates and verification skipping.
type SecurityConfig struct {
	TLSRootCAs    *x509.CertPool
	TLSSkipVerify bool
}

// InternalConfig specifies options for controlling various internal
// items.
// Internal: This should never be used and is not supported.
type InternalConfig struct {
	TLSRootCAProvider func() *x509.CertPool
}

// ClusterOptions is the set of options available for creating a Cluster.
type ClusterOptions struct {
	// Authenticator specifies the authenticator to use with the cluster.
	Authenticator Authenticator

	// Username & Password specifies the cluster username and password to
	// authenticate with.  This is equivalent to passing PasswordAuthenticator
	// as the Authenticator parameter with the same values.
	Username string
	Password string

	// Timeouts specifies various operation timeouts.
	TimeoutsConfig TimeoutsConfig

	// Transcoder is used for trancoding data used in KV operations.
	Transcoder Transcoder

	// RetryStrategy is used to automatically retry operations if they fail.
	RetryStrategy RetryStrategy

	// Tracer specifies the tracer to use for requests.
	// VOLATILE: This API is subject to change at any time.
	Tracer requestTracer

	// OrphanReporterConfig specifies options for the orphan reporter.
	OrphanReporterConfig OrphanReporterConfig

	// CircuitBreakerConfig specifies options for the circuit breakers.
	CircuitBreakerConfig CircuitBreakerConfig

	// IoConfig specifies IO related configuration options.
	IoConfig IoConfig

	// SecurityConfig specifies security related configuration options.
	SecurityConfig SecurityConfig

	// Internal: This should never be used and is not supported.
	InternalConfig InternalConfig
}

// ClusterCloseOptions is the set of options available when
// disconnecting from a Cluster.
type ClusterCloseOptions struct {
}

func clusterFromOptions(opts ClusterOptions) *Cluster {
	if opts.Authenticator == nil {
		opts.Authenticator = PasswordAuthenticator{
			Username: opts.Username,
			Password: opts.Password,
		}
	}

	connectTimeout := 10000 * time.Millisecond
	kvTimeout := 2500 * time.Millisecond
	kvDurableTimeout := 10000 * time.Millisecond
	viewTimeout := 75000 * time.Millisecond
	queryTimeout := 75000 * time.Millisecond
	analyticsTimeout := 75000 * time.Millisecond
	searchTimeout := 75000 * time.Millisecond
	managementTimeout := 75000 * time.Millisecond
	if opts.TimeoutsConfig.ConnectTimeout > 0 {
		connectTimeout = opts.TimeoutsConfig.ConnectTimeout
	}
	if opts.TimeoutsConfig.KVTimeout > 0 {
		kvTimeout = opts.TimeoutsConfig.KVTimeout
	}
	if opts.TimeoutsConfig.KVDurableTimeout > 0 {
		kvDurableTimeout = opts.TimeoutsConfig.KVDurableTimeout
	}
	if opts.TimeoutsConfig.ViewTimeout > 0 {
		viewTimeout = opts.TimeoutsConfig.ViewTimeout
	}
	if opts.TimeoutsConfig.QueryTimeout > 0 {
		queryTimeout = opts.TimeoutsConfig.QueryTimeout
	}
	if opts.TimeoutsConfig.AnalyticsTimeout > 0 {
		analyticsTimeout = opts.TimeoutsConfig.AnalyticsTimeout
	}
	if opts.TimeoutsConfig.SearchTimeout > 0 {
		searchTimeout = opts.TimeoutsConfig.SearchTimeout
	}
	if opts.TimeoutsConfig.ManagementTimeout > 0 {
		managementTimeout = opts.TimeoutsConfig.ManagementTimeout
	}
	if opts.Transcoder == nil {
		opts.Transcoder = NewJSONTranscoder()
	}
	if opts.RetryStrategy == nil {
		opts.RetryStrategy = NewBestEffortRetryStrategy(nil)
	}

	useMutationTokens := true
	useServerDurations := true
	if opts.IoConfig.DisableMutationTokens {
		useMutationTokens = false
	}
	if opts.IoConfig.DisableServerDurations {
		useServerDurations = false
	}

	var initialTracer requestTracer
	if opts.Tracer != nil {
		initialTracer = opts.Tracer
	} else {
		initialTracer = newThresholdLoggingTracer(nil)
	}
	tracerAddRef(initialTracer)

	return &Cluster{
		auth:        opts.Authenticator,
		connections: make(map[string]client),
		sb: stateBlock{
			ConnectTimeout:         connectTimeout,
			QueryTimeout:           queryTimeout,
			AnalyticsTimeout:       analyticsTimeout,
			SearchTimeout:          searchTimeout,
			ViewTimeout:            viewTimeout,
			KvTimeout:              kvTimeout,
			KvDurableTimeout:       kvDurableTimeout,
			DuraPollTimeout:        100 * time.Millisecond,
			Transcoder:             opts.Transcoder,
			UseMutationTokens:      useMutationTokens,
			ManagementTimeout:      managementTimeout,
			RetryStrategyWrapper:   newRetryStrategyWrapper(opts.RetryStrategy),
			OrphanLoggerEnabled:    !opts.OrphanReporterConfig.Disabled,
			OrphanLoggerInterval:   opts.OrphanReporterConfig.ReportInterval,
			OrphanLoggerSampleSize: opts.OrphanReporterConfig.SampleSize,
			UseServerDurations:     useServerDurations,
			Tracer:                 initialTracer,
			CircuitBreakerConfig:   opts.CircuitBreakerConfig,
			SecurityConfig:         opts.SecurityConfig,
			InternalConfig:         opts.InternalConfig,
		},

		queryCache: make(map[string]*queryCacheEntry),
	}
}

// Connect creates and returns a Cluster instance created using the
// provided options and a connection string.
func Connect(connStr string, opts ClusterOptions) (*Cluster, error) {
	connSpec, err := gocbconnstr.Parse(connStr)
	if err != nil {
		return nil, err
	}

	if connSpec.Scheme == "http" {
		return nil, errors.New("http scheme is not supported, use couchbase or couchbases instead")
	}

	cluster := clusterFromOptions(opts)
	cluster.cSpec = connSpec

	err = cluster.parseExtraConnStrOptions(connSpec)
	if err != nil {
		return nil, err
	}

	csb := &clientStateBlock{
		BucketName: "",
	}
	cli := newClient(cluster, csb)
	err = cli.buildConfig()
	if err != nil {
		return nil, err
	}

	err = cli.connect()
	if err != nil {
		return nil, err
	}
	cluster.clusterClient = cli
	cluster.supportsGCCCP = cli.supportsGCCCP()

	return cluster, nil
}

func (c *Cluster) parseExtraConnStrOptions(spec gocbconnstr.ConnSpec) error {
	fetchOption := func(name string) (string, bool) {
		optValue := spec.Options[name]
		if len(optValue) == 0 {
			return "", false
		}
		return optValue[len(optValue)-1], true
	}

	if valStr, ok := fetchOption("query_timeout"); ok {
		val, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			return fmt.Errorf("query_timeout option must be a number")
		}
		c.sb.QueryTimeout = time.Duration(val) * time.Millisecond
	}

	if valStr, ok := fetchOption("analytics_timeout"); ok {
		val, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			return fmt.Errorf("analytics_timeout option must be a number")
		}
		c.sb.AnalyticsTimeout = time.Duration(val) * time.Millisecond
	}

	if valStr, ok := fetchOption("search_timeout"); ok {
		val, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			return fmt.Errorf("search_timeout option must be a number")
		}
		c.sb.SearchTimeout = time.Duration(val) * time.Millisecond
	}

	if valStr, ok := fetchOption("view_timeout"); ok {
		val, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			return fmt.Errorf("view_timeout option must be a number")
		}
		c.sb.ViewTimeout = time.Duration(val) * time.Millisecond
	}

	return nil
}

// Bucket connects the cluster to server(s) and returns a new Bucket instance.
func (c *Cluster) Bucket(bucketName string) *Bucket {
	b := newBucket(&c.sb, bucketName)

	c.connectionsLock.Lock()
	// If the cluster client doesn't support GCCCP then there's no point in keeping this open
	if c.clusterClient != nil && !c.clusterClient.supportsGCCCP() {
		logDebugf("Shutting down cluster level client")
		err := c.clusterClient.close()
		if err != nil {
			logWarnf("Failed to close the cluster level client: %s", err)
		}
		c.clusterClient = nil
		logDebugf("Shut down cluster level client")
	}
	c.connectionsLock.Unlock()

	// First we see if a connection already exists for a bucket with this name.
	cli := c.getClient(&b.sb.clientStateBlock)
	if cli != nil {
		logDebugf("Sharing bucket level connection %p for %s", cli, bucketName)
		b.cacheClient(cli)
		return b
	}

	logDebugf("Creating new bucket level connection for %s", bucketName)
	// A connection doesn't already exist so we need to create a new one.
	cli = newClient(c, &b.sb.clientStateBlock)
	err := cli.buildConfig()
	if err == nil {
		err = cli.connect()
		if err != nil {
			cli.setBootstrapError(err)
		}
	} else {
		cli.setBootstrapError(err)
	}

	c.connectionsLock.Lock()
	c.connections[b.hash()] = cli
	c.connectionsLock.Unlock()
	b.cacheClient(cli)

	return b
}

func (c *Cluster) getClient(sb *clientStateBlock) client {
	c.connectionsLock.Lock()

	hash := sb.Hash()
	if cli, ok := c.connections[hash]; ok {
		c.connectionsLock.Unlock()
		return cli
	}
	c.connectionsLock.Unlock()

	return nil
}

func (c *Cluster) randomClient() (client, error) {
	c.connectionsLock.RLock()
	if len(c.connections) == 0 {
		c.connectionsLock.RUnlock()
		return nil, errors.New("not connected to cluster")
	}
	var randomClient client
	var firstError error
	for _, c := range c.connections { // This is ugly
		err := c.getBootstrapError()
		if err != nil {
			if firstError == nil {
				firstError = c.getBootstrapError()
			}
		} else {
			connected, err := c.connected()
			if err != nil {
				if firstError == nil {
					firstError = err
				}
			} else if connected {
				randomClient = c
				break
			}
		}
	}
	c.connectionsLock.RUnlock()
	if randomClient == nil {
		if firstError == nil {
			return nil, errors.New("not connected to cluster")
		}

		return nil, firstError
	}

	return randomClient, nil
}

func (c *Cluster) authenticator() Authenticator {
	return c.auth
}

func (c *Cluster) connSpec() gocbconnstr.ConnSpec {
	return c.cSpec
}

// WaitUntilReadyOptions is the set of options available to the WaitUntilReady operations.
type WaitUntilReadyOptions struct {
	DesiredState ClusterState
}

// WaitUntilReady will wait for the cluster object to be ready for use.
// At present this will wait until memd connections have been established with the server and are ready
// to be used.
func (c *Cluster) WaitUntilReady(timeout time.Duration, opts *WaitUntilReadyOptions) error {
	if opts == nil {
		opts = &WaitUntilReadyOptions{}
	}

	cli := c.clusterClient
	if cli == nil {
		return errors.New("cluster is not connected")
	}

	provider, err := cli.getWaitUntilReadyProvider()
	if err != nil {
		return err
	}

	desiredState := opts.DesiredState
	if desiredState == 0 {
		desiredState = ClusterStateOnline
	}

	err = provider.WaitUntilReady(
		time.Now().Add(timeout),
		gocbcore.WaitUntilReadyOptions{
			DesiredState: gocbcore.ClusterState(desiredState),
		},
	)
	if err != nil {
		return err
	}

	return nil
}

// Close shuts down all buckets in this cluster and invalidates any references this cluster has.
func (c *Cluster) Close(opts *ClusterCloseOptions) error {
	var overallErr error

	c.clusterLock.Lock()
	for key, conn := range c.connections {
		err := conn.close()
		if err != nil {
			logWarnf("Failed to close a client in cluster close: %s", err)
			overallErr = err
		}

		delete(c.connections, key)
	}
	if c.clusterClient != nil {
		err := c.clusterClient.close()
		if err != nil {
			logWarnf("Failed to close cluster client in cluster close: %s", err)
			overallErr = err
		}
	}
	c.clusterLock.Unlock()

	if c.sb.Tracer != nil {
		tracerDecRef(c.sb.Tracer)
		c.sb.Tracer = nil
	}

	return overallErr
}

func (c *Cluster) clusterOrRandomClient() (client, error) {
	var cli client
	c.connectionsLock.RLock()
	if c.clusterClient == nil {
		c.connectionsLock.RUnlock()
		var err error
		cli, err = c.randomClient()
		if err != nil {
			return nil, err
		}
	} else {
		cli = c.clusterClient
		c.connectionsLock.RUnlock()
		if !cli.supportsGCCCP() {
			return nil, errors.New("the cluster does not support cluster-level queries " +
				"(only Couchbase Server 6.5 and later) and no bucket is open. If an older Couchbase Server version " +
				"is used, at least one bucket needs to be opened")
		}
	}

	return cli, nil
}

func (c *Cluster) getDiagnosticsProvider() (diagnosticsProvider, error) {
	cli, err := c.clusterOrRandomClient()
	if err != nil {
		return nil, err
	}

	provider, err := cli.getDiagnosticsProvider()
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (c *Cluster) getQueryProvider() (queryProvider, error) {
	cli, err := c.clusterOrRandomClient()
	if err != nil {
		return nil, err
	}

	provider, err := cli.getQueryProvider()
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (c *Cluster) getAnalyticsProvider() (analyticsProvider, error) {
	cli, err := c.clusterOrRandomClient()
	if err != nil {
		return nil, err
	}

	provider, err := cli.getAnalyticsProvider()
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (c *Cluster) getSearchProvider() (searchProvider, error) {
	cli, err := c.clusterOrRandomClient()
	if err != nil {
		return nil, err
	}

	provider, err := cli.getSearchProvider()
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (c *Cluster) getHTTPProvider() (httpProvider, error) {
	cli, err := c.clusterOrRandomClient()
	if err != nil {
		return nil, err
	}

	provider, err := cli.getHTTPProvider()
	if err != nil {
		return nil, err
	}

	return provider, nil
}

// Users returns a UserManager for managing users.
func (c *Cluster) Users() *UserManager {
	return &UserManager{
		provider: c,
		tracer:   c.sb.Tracer,
	}
}

// Buckets returns a BucketManager for managing buckets.
func (c *Cluster) Buckets() *BucketManager {
	return &BucketManager{
		provider: c,
		tracer:   c.sb.Tracer,
	}
}

// AnalyticsIndexes returns an AnalyticsIndexManager for managing analytics indexes.
func (c *Cluster) AnalyticsIndexes() *AnalyticsIndexManager {
	return &AnalyticsIndexManager{
		aProvider:     c,
		mgmtProvider:  c,
		globalTimeout: c.sb.ManagementTimeout,
		tracer:        c.sb.Tracer,
	}
}

// QueryIndexes returns a QueryIndexManager for managing query indexes.
func (c *Cluster) QueryIndexes() *QueryIndexManager {
	return &QueryIndexManager{
		provider:      c,
		globalTimeout: c.sb.ManagementTimeout,
		tracer:        c.sb.Tracer,
	}
}

// SearchIndexes returns a SearchIndexManager for managing search indexes.
func (c *Cluster) SearchIndexes() *SearchIndexManager {
	return &SearchIndexManager{
		mgmtProvider: c,
		tracer:       c.sb.Tracer,
	}
}
