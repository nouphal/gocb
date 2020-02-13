package gocb

import (
	"time"
)

func (suite *IntegrationTestSuite) TestQueryIndexesCrud() {
	if !globalCluster.SupportsFeature(QueryFeature) {
		suite.T().Skip("Skipping test, query indexes not supported.")
	}

	bucketMgr := globalCluster.Buckets()
	bucketName := "testIndexes"

	err := bucketMgr.CreateBucket(CreateBucketSettings{
		BucketSettings: BucketSettings{
			Name:        bucketName,
			RAMQuotaMB:  100,
			NumReplicas: 0,
			BucketType:  CouchbaseBucketType,
		},
	}, nil)
	if err != nil {
		suite.T().Fatalf("Failed to create bucket manager %v", err)
	}
	defer bucketMgr.DropBucket(bucketName, nil)

	mgr := globalCluster.QueryIndexes()

	err = mgr.CreatePrimaryIndex(bucketName, &CreatePrimaryQueryIndexOptions{
		IgnoreIfExists: true,
	})
	if err != nil {
		suite.T().Fatalf("Expected CreatePrimaryIndex to not error %v", err)
	}

	err = mgr.CreatePrimaryIndex(bucketName, &CreatePrimaryQueryIndexOptions{
		IgnoreIfExists: false,
	})
	if err == nil {
		suite.T().Fatalf("Expected CreatePrimaryIndex to error")
	}

	err = mgr.CreateIndex(bucketName, "testIndex", []string{"field"}, &CreateQueryIndexOptions{
		IgnoreIfExists: true,
	})
	if err != nil {
		suite.T().Fatalf("Expected CreateIndex to not error %v", err)
	}

	err = mgr.CreateIndex(bucketName, "testIndex", []string{"field"}, &CreateQueryIndexOptions{
		IgnoreIfExists: false,
	})
	if err == nil {
		suite.T().Fatalf("Expected CreateIndex to error")
	}

	// We create this first to give it a chance to be created by the time we need it.
	err = mgr.CreateIndex(bucketName, "testIndexDeferred", []string{"field"}, &CreateQueryIndexOptions{
		IgnoreIfExists: false,
		Deferred:       true,
	})
	if err != nil {
		suite.T().Fatalf("Expected CreateIndex to not error %v", err)
	}

	indexNames, err := mgr.BuildDeferredIndexes(bucketName, nil)
	if err != nil {
		suite.T().Fatalf("Expected BuildDeferredIndexes to not error %v", err)
	}

	if len(indexNames) != 1 {
		suite.T().Fatalf("Expected 1 index but was %d", len(indexNames))
	}

	err = mgr.WatchIndexes(bucketName, []string{"testIndexDeferred"}, 5*time.Second, nil)
	if err != nil {
		suite.T().Fatalf("Expected WatchIndexes to not error %v", err)
	}

	indexes, err := mgr.GetAllIndexes(bucketName, nil)
	if err != nil {
		suite.T().Fatalf("Expected GetAllIndexes to not error but was %v", err)
	}

	if len(indexes) != 3 {
		suite.T().Fatalf("Expected 3 indexes but was %d", len(indexes))
	}

	err = mgr.DropIndex(bucketName, "testIndex", nil)
	if err != nil {
		suite.T().Fatalf("Expected DropIndex to not error %v", err)
	}

	err = mgr.DropIndex(bucketName, "testIndex", nil)
	if err == nil {
		suite.T().Fatalf("Expected DropIndex to error")
	}

	err = mgr.DropPrimaryIndex(bucketName, nil)
	if err != nil {
		suite.T().Fatalf("Expected DropPrimaryIndex to not error %v", err)
	}

	err = mgr.DropPrimaryIndex(bucketName, nil)
	if err == nil {
		suite.T().Fatalf("Expected DropPrimaryIndex to error")
	}
}