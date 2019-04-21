// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package worker

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/blobstore"
	"github.com/uber/cadence/common/cache"
	"github.com/uber/cadence/common/log"
	"github.com/uber/cadence/common/log/loggerimpl"
	"github.com/uber/cadence/common/log/tag"
	"github.com/uber/cadence/common/metrics"
	persistencefactory "github.com/uber/cadence/common/persistence/persistence-factory"
	"github.com/uber/cadence/common/service"
	"github.com/uber/cadence/common/service/config"
	"github.com/uber/cadence/common/service/dynamicconfig"
	"github.com/uber/cadence/service/worker/archiver"
	"github.com/uber/cadence/service/worker/indexer"
	"github.com/uber/cadence/service/worker/replicator"
	"github.com/uber/cadence/service/worker/scanner"
	"go.uber.org/cadence/.gen/go/cadence/workflowserviceclient"
	"go.uber.org/cadence/client"
)

type (
	// Service represents the cadence-worker service. This service hosts all background processing needed for cadence cluster:
	// 1. Replicator: Handles applying replication tasks generated by remote clusters.
	// 2. Indexer: Handles uploading of visibility records to elastic search.
	// 3. Archiver: Handles archival of workflow histories.
	Service struct {
		stopC         chan struct{}
		isStopped     int32
		params        *service.BootstrapParams
		config        *Config
		logger        log.Logger
		metricsClient metrics.Client
	}

	// Config contains all the service config for worker
	Config struct {
		ReplicationCfg  *replicator.Config
		ArchiverConfig  *archiver.Config
		IndexerCfg      *indexer.Config
		ScannerCfg      *scanner.Config
		ThrottledLogRPS dynamicconfig.IntPropertyFn
	}
)

// NewService builds a new cadence-worker service
func NewService(params *service.BootstrapParams) common.Daemon {
	params.UpdateLoggerWithServiceName(common.WorkerServiceName)
	config := NewConfig(params)
	params.ThrottledLogger = loggerimpl.NewThrottledLogger(params.Logger, config.ThrottledLogRPS)
	return &Service{
		params: params,
		config: config,
		stopC:  make(chan struct{}),
	}
}

// NewConfig builds the new Config for cadence-worker service
func NewConfig(params *service.BootstrapParams) *Config {
	dc := dynamicconfig.NewCollection(params.DynamicConfig, params.Logger)
	return &Config{
		ReplicationCfg: &replicator.Config{
			PersistenceMaxQPS:                 dc.GetIntProperty(dynamicconfig.WorkerPersistenceMaxQPS, 500),
			ReplicatorMetaTaskConcurrency:     dc.GetIntProperty(dynamicconfig.WorkerReplicatorMetaTaskConcurrency, 64),
			ReplicatorTaskConcurrency:         dc.GetIntProperty(dynamicconfig.WorkerReplicatorTaskConcurrency, 256),
			ReplicatorMessageConcurrency:      dc.GetIntProperty(dynamicconfig.WorkerReplicatorMessageConcurrency, 2048),
			ReplicatorHistoryBufferRetryCount: dc.GetIntProperty(dynamicconfig.WorkerReplicatorHistoryBufferRetryCount, 8),
			ReplicationTaskMaxRetry:           dc.GetIntProperty(dynamicconfig.WorkerReplicationTaskMaxRetry, 400),
		},
		ArchiverConfig: &archiver.Config{
			EnableArchivalCompression:                 dc.GetBoolPropertyFnWithDomainFilter(dynamicconfig.EnableArchivalCompression, true),
			HistoryPageSize:                           dc.GetIntPropertyFilteredByDomain(dynamicconfig.WorkerHistoryPageSize, 250),
			TargetArchivalBlobSize:                    dc.GetIntPropertyFilteredByDomain(dynamicconfig.WorkerTargetArchivalBlobSize, 2*1024*1024), // 2MB
			ArchiverConcurrency:                       dc.GetIntProperty(dynamicconfig.WorkerArchiverConcurrency, 50),
			ArchivalsPerIteration:                     dc.GetIntProperty(dynamicconfig.WorkerArchivalsPerIteration, 1000),
			DeterministicConstructionCheckProbability: dc.GetFloat64Property(dynamicconfig.WorkerDeterministicConstructionCheckProbability, 0.002),
		},
		IndexerCfg: &indexer.Config{
			IndexerConcurrency:       dc.GetIntProperty(dynamicconfig.WorkerIndexerConcurrency, 1000),
			ESProcessorNumOfWorkers:  dc.GetIntProperty(dynamicconfig.WorkerESProcessorNumOfWorkers, 1),
			ESProcessorBulkActions:   dc.GetIntProperty(dynamicconfig.WorkerESProcessorBulkActions, 1000),
			ESProcessorBulkSize:      dc.GetIntProperty(dynamicconfig.WorkerESProcessorBulkSize, 2<<24), // 16MB
			ESProcessorFlushInterval: dc.GetDurationProperty(dynamicconfig.WorkerESProcessorFlushInterval, 1*time.Second),
		},
		ScannerCfg: &scanner.Config{
			PersistenceMaxQPS: dc.GetIntProperty(dynamicconfig.ScannerPersistenceMaxQPS, 100),
			Persistence:       &params.PersistenceConfig,
			ClusterMetadata:   params.ClusterMetadata,
		},
		ThrottledLogRPS: dc.GetIntProperty(dynamicconfig.WorkerThrottledLogRPS, 20),
	}
}

// Start is called to start the service
func (s *Service) Start() {
	base := service.New(s.params)
	base.Start()
	s.logger = base.GetLogger()
	s.metricsClient = base.GetMetricsClient()
	s.logger.Info("service starting", tag.ComponentWorker)

	pConfig := s.params.PersistenceConfig
	pConfig.SetMaxQPS(pConfig.DefaultStore, s.config.ReplicationCfg.PersistenceMaxQPS())
	pFactory := persistencefactory.New(&pConfig, s.params.ClusterMetadata.GetCurrentClusterName(), s.metricsClient, s.logger)

	if base.GetClusterMetadata().IsGlobalDomainEnabled() {
		s.startReplicator(base, pFactory)
	}
	if base.GetClusterMetadata().ArchivalConfig().ConfiguredForArchival() {
		s.startArchiver(base, pFactory)
	}
	if s.params.ESConfig.Enable {
		s.startIndexer(base)
	}

	s.startScanner(base)

	s.logger.Info("service started", tag.ComponentWorker)
	<-s.stopC
	base.Stop()
}

// Stop is called to stop the service
func (s *Service) Stop() {
	if !atomic.CompareAndSwapInt32(&s.isStopped, 0, 1) {
		return
	}
	close(s.stopC)
	s.params.Logger.Info("service stopped", tag.ComponentWorker)
}

func (s *Service) startScanner(base service.Service) {
	storeType := s.config.ScannerCfg.Persistence.DefaultStoreType()
	if storeType != config.StoreTypeSQL {
		s.logger.Info("Scanner not started: incompatible persistence store type", tag.StoreType(storeType))
		return
	}
	params := &scanner.BootstrapParams{
		Config:        *s.config.ScannerCfg,
		SDKClient:     s.params.PublicClient,
		MetricsClient: s.metricsClient,
		Logger:        s.logger,
		TallyScope:    s.params.MetricScope,
	}
	scanner := scanner.New(params)
	if err := scanner.Start(); err != nil {
		s.logger.Fatal("error starting scanner:%v", tag.Error(err))
	}
}

func (s *Service) startReplicator(base service.Service, pFactory persistencefactory.Factory) {
	metadataV2Mgr, err := pFactory.NewMetadataManager(persistencefactory.MetadataV2)
	if err != nil {
		s.logger.Fatal("failed to start replicator, could not create MetadataManager", tag.Error(err))
	}
	domainCache := cache.NewDomainCache(metadataV2Mgr, base.GetClusterMetadata(), s.metricsClient, s.logger)
	domainCache.Start()

	replicator := replicator.NewReplicator(
		base.GetClusterMetadata(),
		metadataV2Mgr,
		domainCache,
		base.GetClientBean(),
		s.config.ReplicationCfg,
		base.GetMessagingClient(),
		s.logger,
		s.metricsClient)
	if err := replicator.Start(); err != nil {
		replicator.Stop()
		s.logger.Fatal("fail to start replicator", tag.Error(err))
	}
}

func (s *Service) startIndexer(base service.Service) {
	indexer := indexer.NewIndexer(
		s.config.IndexerCfg,
		base.GetMessagingClient(),
		s.params.ESClient,
		s.params.ESConfig,
		s.logger,
		s.metricsClient)
	if err := indexer.Start(); err != nil {
		indexer.Stop()
		s.logger.Fatal("fail to start indexer", tag.Error(err))
	}
}

func (s *Service) startArchiver(base service.Service, pFactory persistencefactory.Factory) {
	publicClient := s.params.PublicClient
	s.ensureSystemDomainExists(publicClient)

	historyManager, err := pFactory.NewHistoryManager()
	if err != nil {
		s.logger.Fatal("failed to start archiver, could not create HistoryManager", tag.Error(err))
	}
	historyV2Manager, err := pFactory.NewHistoryV2Manager()
	if err != nil {
		s.logger.Fatal("failed to start archiver, could not create HistoryV2Manager", tag.Error(err))
	}
	metadataMgr, err := pFactory.NewMetadataManager(persistencefactory.MetadataV1V2)
	if err != nil {
		s.logger.Fatal("failed to start archiver, could not create MetadataManager", tag.Error(err))
	}
	domainCache := cache.NewDomainCache(metadataMgr, s.params.ClusterMetadata, s.metricsClient, s.logger)
	domainCache.Start()

	blobstoreClient := blobstore.NewRetryableClient(
		blobstore.NewMetricClient(s.params.BlobstoreClient, s.metricsClient),
		s.params.BlobstoreClient.GetRetryPolicy(),
		s.params.BlobstoreClient.IsRetryableError)

	bc := &archiver.BootstrapContainer{
		PublicClient:     publicClient,
		MetricsClient:    s.metricsClient,
		Logger:           s.logger,
		ClusterMetadata:  base.GetClusterMetadata(),
		HistoryManager:   historyManager,
		HistoryV2Manager: historyV2Manager,
		Blobstore:        blobstoreClient,
		DomainCache:      domainCache,
		Config:           s.config.ArchiverConfig,
	}
	clientWorker := archiver.NewClientWorker(bc)
	if err := clientWorker.Start(); err != nil {
		clientWorker.Stop()
		s.logger.Fatal("failed to start archiver", tag.Error(err))
	}
}

func (s *Service) ensureSystemDomainExists(publicClient workflowserviceclient.Interface) {
	domainClient := client.NewDomainClient(publicClient, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := domainClient.Describe(ctx, common.SystemDomainName)
	if err != nil {
		s.logger.Fatal("failed to verify that cadence system domain exists", tag.Error(err))
	}
}
