package txmgr_test

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink/core/assets"
	"github.com/smartcontractkit/chainlink/core/chains/evm/txmgr"
	"github.com/smartcontractkit/chainlink/core/internal/cltest"
	configtest "github.com/smartcontractkit/chainlink/core/internal/testutils/configtest/v2"
	"github.com/smartcontractkit/chainlink/core/internal/testutils/evmtest"
	"github.com/smartcontractkit/chainlink/core/internal/testutils/pgtest"
	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/smartcontractkit/chainlink/core/services/chainlink"
	"github.com/smartcontractkit/chainlink/core/store/models"
	"github.com/smartcontractkit/chainlink/core/utils"
)

func Test_EthResender_FindEthTxAttemptsRequiringResend(t *testing.T) {
	t.Parallel()

	db := pgtest.NewSqlxDB(t)
	logCfg := pgtest.NewQConfig(true)
	borm := cltest.NewTxmORM(t, db, logCfg)

	ethKeyStore := cltest.NewKeyStore(t, db, logCfg).Eth()

	_, fromAddress := cltest.MustInsertRandomKey(t, ethKeyStore)

	t.Run("returns nothing if there are no transactions", func(t *testing.T) {
		olderThan := time.Now()
		attempts, err := txmgr.FindEthTxAttemptsRequiringResend(db, olderThan, 10, cltest.FixtureChainID, fromAddress)
		require.NoError(t, err)
		assert.Len(t, attempts, 0)
	})

	etxs := []txmgr.EthTx{
		cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 0, fromAddress, time.Unix(1616509100, 0)),
		cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 1, fromAddress, time.Unix(1616509200, 0)),
		cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 2, fromAddress, time.Unix(1616509300, 0)),
		cltest.MustInsertUnconfirmedEthTxWithBroadcastDynamicFeeAttempt(t, borm, 3, fromAddress, time.Unix(1616509400, 0)),
	}
	attempt1_2 := newBroadcastLegacyEthTxAttempt(t, etxs[0].ID)
	attempt1_2.GasPrice = assets.NewWeiI(10)
	require.NoError(t, borm.InsertEthTxAttempt(&attempt1_2))

	attempt3_2 := newInProgressLegacyEthTxAttempt(t, etxs[2].ID)
	attempt3_2.GasPrice = assets.NewWeiI(10)
	require.NoError(t, borm.InsertEthTxAttempt(&attempt3_2))

	attempt4_2 := cltest.NewDynamicFeeEthTxAttempt(t, etxs[3].ID)
	attempt4_2.GasTipCap = assets.NewWeiI(10)
	attempt4_2.GasFeeCap = assets.NewWeiI(20)
	attempt4_2.State = txmgr.EthTxAttemptBroadcast
	require.NoError(t, borm.InsertEthTxAttempt(&attempt4_2))
	attempt4_4 := cltest.NewDynamicFeeEthTxAttempt(t, etxs[3].ID)
	attempt4_4.GasTipCap = assets.NewWeiI(30)
	attempt4_4.GasFeeCap = assets.NewWeiI(40)
	attempt4_4.State = txmgr.EthTxAttemptBroadcast
	require.NoError(t, borm.InsertEthTxAttempt(&attempt4_4))
	attempt4_3 := cltest.NewDynamicFeeEthTxAttempt(t, etxs[3].ID)
	attempt4_3.GasTipCap = assets.NewWeiI(20)
	attempt4_3.GasFeeCap = assets.NewWeiI(30)
	attempt4_3.State = txmgr.EthTxAttemptBroadcast
	require.NoError(t, borm.InsertEthTxAttempt(&attempt4_3))

	t.Run("returns nothing if there are transactions from a different key", func(t *testing.T) {
		olderThan := time.Now()
		attempts, err := txmgr.FindEthTxAttemptsRequiringResend(db, olderThan, 10, cltest.FixtureChainID, utils.RandomAddress())
		require.NoError(t, err)
		assert.Len(t, attempts, 0)
	})

	t.Run("returns the highest price attempt for each transaction that was last broadcast before or on the given time", func(t *testing.T) {
		olderThan := time.Unix(1616509200, 0)
		attempts, err := txmgr.FindEthTxAttemptsRequiringResend(db, olderThan, 0, cltest.FixtureChainID, fromAddress)
		require.NoError(t, err)
		assert.Len(t, attempts, 2)
		assert.Equal(t, attempt1_2.ID, attempts[0].ID)
		assert.Equal(t, etxs[1].EthTxAttempts[0].ID, attempts[1].ID)
	})

	t.Run("returns the highest price attempt for EIP-1559 transactions", func(t *testing.T) {
		olderThan := time.Unix(1616509400, 0)
		attempts, err := txmgr.FindEthTxAttemptsRequiringResend(db, olderThan, 0, cltest.FixtureChainID, fromAddress)
		require.NoError(t, err)
		assert.Len(t, attempts, 4)
		assert.Equal(t, attempt4_4.ID, attempts[3].ID)
	})

	t.Run("applies limit", func(t *testing.T) {
		olderThan := time.Unix(1616509200, 0)
		attempts, err := txmgr.FindEthTxAttemptsRequiringResend(db, olderThan, 1, cltest.FixtureChainID, fromAddress)
		require.NoError(t, err)
		assert.Len(t, attempts, 1)
		assert.Equal(t, attempt1_2.ID, attempts[0].ID)
	})
}

func Test_EthResender_resendUnconfirmed(t *testing.T) {
	t.Parallel()

	db := pgtest.NewSqlxDB(t)
	logCfg := pgtest.NewQConfig(true)
	lggr := logger.TestLogger(t)
	ethKeyStore := cltest.NewKeyStore(t, db, logCfg).Eth()
	ethClient := evmtest.NewEthClientMockWithDefaultChain(t)
	cfg := configtest.NewGeneralConfig(t, func(c *chainlink.Config, s *chainlink.Secrets) {})
	evmcfg := evmtest.NewChainScopedConfig(t, cfg)

	_, fromAddress := cltest.MustInsertRandomKey(t, ethKeyStore)
	_, fromAddress2 := cltest.MustInsertRandomKey(t, ethKeyStore)
	_, fromAddress3 := cltest.MustInsertRandomKey(t, ethKeyStore)

	borm := cltest.NewTxmORM(t, db, logCfg)

	originalBroadcastAt := time.Unix(1616509100, 0)

	var addr1TxesRawHex, addr2TxesRawHex, addr3TxesRawHex []string
	// fewer than EvmMaxInFlightTransactions
	for i := uint32(0); i < evmcfg.EvmMaxInFlightTransactions()/2; i++ {
		etx := cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, int64(i), fromAddress, originalBroadcastAt)
		addr1TxesRawHex = append(addr1TxesRawHex, hexutil.Encode(etx.EthTxAttempts[0].SignedRawTx))
	}

	// exactly EvmMaxInFlightTransactions
	for i := uint32(0); i < evmcfg.EvmMaxInFlightTransactions(); i++ {
		etx := cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, int64(i), fromAddress2, originalBroadcastAt)
		addr2TxesRawHex = append(addr2TxesRawHex, hexutil.Encode(etx.EthTxAttempts[0].SignedRawTx))
	}

	// more than EvmMaxInFlightTransactions
	for i := uint32(0); i < evmcfg.EvmMaxInFlightTransactions()*2; i++ {
		etx := cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, int64(i), fromAddress3, originalBroadcastAt)
		addr3TxesRawHex = append(addr3TxesRawHex, hexutil.Encode(etx.EthTxAttempts[0].SignedRawTx))
	}

	er := txmgr.NewEthResender(lggr, db, ethClient, ethKeyStore, 100*time.Millisecond, evmcfg)

	t.Run("sends up to EvmMaxInFlightTransactions per key", func(t *testing.T) {
		ethClient.On("BatchCallContextAll", mock.Anything, mock.MatchedBy(func(elems []rpc.BatchElem) bool {
			resentHex := make([]string, len(elems))
			for i, elem := range elems {
				resentHex[i] = elem.Args[0].(string)
			}
			assert.Len(t, elems, len(addr1TxesRawHex)+len(addr2TxesRawHex)+int(evmcfg.EvmMaxInFlightTransactions()))
			// All addr1TxesRawHex should be included
			for _, addr := range addr1TxesRawHex {
				assert.Contains(t, resentHex, addr)
			}
			// All addr2TxesRawHex should be included
			for _, addr := range addr1TxesRawHex {
				assert.Contains(t, resentHex, addr)
			}
			// Up to limit EvmMaxInFlightTransactions addr3TxesRawHex should be included
			for i, addr := range addr1TxesRawHex {
				if i > int(evmcfg.EvmMaxInFlightTransactions()) {
					// Above limit EvmMaxInFlightTransactions addr3TxesRawHex should NOT be included
					assert.NotContains(t, resentHex, addr)
				} else {
					assert.Contains(t, resentHex, addr)
				}
			}
			return true
		})).Run(func(args mock.Arguments) {}).Return(nil)
		err := er.ResendUnconfirmed()
		require.NoError(t, err)

	})
}

func Test_EthResender_Start(t *testing.T) {
	t.Parallel()

	db := pgtest.NewSqlxDB(t)
	cfg := configtest.NewGeneralConfig(t, func(c *chainlink.Config, s *chainlink.Secrets) {
		// This can be anything as long as it isn't zero
		c.EVM[0].Transactions.ResendAfterThreshold = models.MustNewDuration(42 * time.Hour)
		// Set batch size low to test batching
		c.EVM[0].RPCDefaultBatchSize = ptr[uint32](1)
	})
	borm := cltest.NewTxmORM(t, db, cfg)
	ethKeyStore := cltest.NewKeyStore(t, db, cfg).Eth()
	evmcfg := evmtest.NewChainScopedConfig(t, cfg)
	_, fromAddress := cltest.MustInsertRandomKey(t, ethKeyStore)
	lggr := logger.TestLogger(t)

	t.Run("resends transactions that have been languishing unconfirmed for too long", func(t *testing.T) {
		ethClient := evmtest.NewEthClientMockWithDefaultChain(t)

		er := txmgr.NewEthResender(lggr, db, ethClient, ethKeyStore, 100*time.Millisecond, evmcfg)

		originalBroadcastAt := time.Unix(1616509100, 0)
		etx := cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 0, fromAddress, originalBroadcastAt)
		etx2 := cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 1, fromAddress, originalBroadcastAt)
		cltest.MustInsertUnconfirmedEthTxWithBroadcastLegacyAttempt(t, borm, 2, fromAddress, time.Now().Add(1*time.Hour))

		// First batch of 1
		ethClient.On("BatchCallContextAll", mock.Anything, mock.MatchedBy(func(b []rpc.BatchElem) bool {
			return len(b) == 1 &&
				b[0].Method == "eth_sendRawTransaction" && b[0].Args[0] == hexutil.Encode(etx.EthTxAttempts[0].SignedRawTx)
		})).Return(nil)
		// Second batch of 1
		ethClient.On("BatchCallContextAll", mock.Anything, mock.MatchedBy(func(b []rpc.BatchElem) bool {
			return len(b) == 1 &&
				b[0].Method == "eth_sendRawTransaction" && b[0].Args[0] == hexutil.Encode(etx2.EthTxAttempts[0].SignedRawTx)
		})).Return(nil).Run(func(args mock.Arguments) {
			elems := args.Get(1).([]rpc.BatchElem)
			// It should update BroadcastAt even if there is an error here
			elems[0].Error = errors.New("kaboom")
		})

		func() {
			er.Start()
			defer er.Stop()

			cltest.EventuallyExpectationsMet(t, ethClient, 5*time.Second, time.Second)
		}()

		err := db.Get(&etx, `SELECT * FROM eth_txes WHERE id = $1`, etx.ID)
		require.NoError(t, err)
		err = db.Get(&etx2, `SELECT * FROM eth_txes WHERE id = $1`, etx2.ID)
		require.NoError(t, err)

		assert.Greater(t, etx.BroadcastAt.Unix(), originalBroadcastAt.Unix())
		assert.Greater(t, etx2.BroadcastAt.Unix(), originalBroadcastAt.Unix())
	})
}
