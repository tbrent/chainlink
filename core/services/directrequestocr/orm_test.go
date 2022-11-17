package directrequestocr_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/sqlx"

	"github.com/smartcontractkit/chainlink/core/internal/testutils"
	"github.com/smartcontractkit/chainlink/core/internal/testutils/pgtest"
	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/smartcontractkit/chainlink/core/services/directrequestocr"
)

type TestORM struct {
	directrequestocr.ORM

	db *sqlx.DB
}

func setupORM(t *testing.T) *TestORM {
	t.Helper()

	var (
		db   = pgtest.NewSqlxDB(t)
		lggr = logger.TestLogger(t)
		orm  = directrequestocr.NewORM(db, lggr, pgtest.NewQConfig(true))
	)

	return &TestORM{ORM: orm, db: db}
}

func reqID(id uint) directrequestocr.RequestID {
	byteArr := (*[32]byte)([]byte(fmt.Sprintf("%032d\n", id)))
	return *byteArr
}

func TestDROCROrm_CreateRequestDuplicate(t *testing.T) {
	t.Parallel()

	orm := setupORM(t)
	id := reqID(420)
	txHash := common.HexToHash("0xabc")
	addr := testutils.NewAddress()

	err := orm.CreateRequest(id, &addr, time.Now(), &txHash)
	require.NoError(t, err)
	err = orm.CreateRequest(id, &addr, time.Now(), &txHash)
	require.NotNil(t, err)
}

func TestDROCROrm_FindOldestEntriesByStateWithLimit(t *testing.T) {
	t.Parallel()

	orm := setupORM(t)
	id1, id2, id3 := reqID(101), reqID(102), reqID(103)
	txHash := common.HexToHash("0xabc")
	addr := testutils.NewAddress()
	ts := time.Now()

	err := orm.CreateRequest(id2, &addr, ts.Add(time.Minute*2), &txHash)
	require.NoError(t, err)
	err = orm.CreateRequest(id3, &addr, ts.Add(time.Minute*3), &txHash)
	require.NoError(t, err)
	err = orm.CreateRequest(id1, &addr, ts.Add(time.Minute*1), &txHash)
	require.NoError(t, err)

	result, err := orm.FindOldestEntriesByState(directrequestocr.IN_PROGRESS, 2)
	require.NoError(t, err)
	require.Equal(t, 2, len(result), "incorrect result length")
	require.Equal(t, id1, result[0].RequestID, "incorrect item in results")
	require.Equal(t, id2, result[1].RequestID, "incorrect item in results")

	result, err = orm.FindOldestEntriesByState(directrequestocr.IN_PROGRESS, 20)
	require.NoError(t, err)
	require.Equal(t, 3, len(result), "incorrect result length")
}
