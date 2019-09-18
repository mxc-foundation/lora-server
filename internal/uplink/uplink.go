package uplink

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/brocaar/loraserver/api/gw"
	gwbackend "github.com/brocaar/loraserver/internal/backend/gateway"
	"github.com/brocaar/loraserver/internal/config"
	"github.com/brocaar/loraserver/internal/downlink/ack"
	"github.com/brocaar/loraserver/internal/framelog"
	"github.com/brocaar/loraserver/internal/gateway"
	"github.com/brocaar/loraserver/internal/helpers"
	"github.com/brocaar/loraserver/internal/logging"
	"github.com/brocaar/loraserver/internal/models"
	"github.com/brocaar/loraserver/internal/storage"
	"github.com/brocaar/loraserver/internal/uplink/data"
	"github.com/brocaar/loraserver/internal/uplink/join"
	"github.com/brocaar/loraserver/internal/uplink/proprietary"
	"github.com/brocaar/loraserver/internal/uplink/rejoin"
	"github.com/brocaar/lorawan"
)

var (
	deduplicationDelay time.Duration
)

// Setup configures the package.
func Setup(conf config.Config) error {
	if err := data.Setup(conf); err != nil {
		return errors.Wrap(err, "configure uplink/data error")
	}

	if err := join.Setup(conf); err != nil {
		return errors.Wrap(err, "configure uplink/join error")
	}

	if err := rejoin.Setup(conf); err != nil {
		return errors.Wrap(err, "configure uplink/rejoin error")
	}

	deduplicationDelay = conf.NetworkServer.DeduplicationDelay

	return nil
}

// Server represents a server listening for uplink packets.
type Server struct {
	wg sync.WaitGroup
}

// NewServer creates a new server.
func NewServer() *Server {
	return &Server{}
}

// Start starts the server.
func (s *Server) Start() error {
	go func() {
		s.wg.Add(1)
		defer s.wg.Done()
		HandleUplinkFrames(&s.wg)
	}()

	go func() {
		s.wg.Add(1)
		defer s.wg.Done()
		HandleDownlinkTXAcks(&s.wg)
	}()
	return nil
}

// Stop closes the gateway backend and waits for the server to complete the
// pending packets.
func (s *Server) Stop() error {
	if err := gwbackend.Backend().Close(); err != nil {
		return fmt.Errorf("close gateway backend error: %s", err)
	}
	log.Info("uplink: waiting for pending actions to complete")
	s.wg.Wait()
	return nil
}

// HandleUplinkFrames consumes received packets by the gateway and handles them
// in a separate go-routine. Errors are logged.
func HandleUplinkFrames(wg *sync.WaitGroup) {
	for uplinkFrame := range gwbackend.Backend().RXPacketChan() {
		go func(uplinkFrame gw.UplinkFrame) {
			wg.Add(1)
			defer wg.Done()

			// The ctxID will be available as context value "ctx_id" so that
			// this can be used when writing logs. This makes it easier to
			// group multiple log-lines to the same context.
			ctxID, err := uuid.NewV4()
			if err != nil {
				log.WithError(err).Error("uplink: get new uuid error")
			}

			ctx := context.Background()
			ctx = context.WithValue(ctx, logging.ContextIDKey, ctxID)

			if err := HandleUplinkFrame(ctx, uplinkFrame); err != nil {
				log.WithFields(log.Fields{
					"ctx_id": ctxID,
				}).WithError(err).Error("uplink: processing uplink frame error")
			}
		}(uplinkFrame)
	}
}

// HandleUplinkFrame handles a single uplink frame.
func HandleUplinkFrame(ctx context.Context, uplinkFrame gw.UplinkFrame) error {
	return collectUplinkFrames(ctx, uplinkFrame)
}

// HandleDownlinkTXAcks consumes received downlink tx acknowledgements from
// the gateway.
func HandleDownlinkTXAcks(wg *sync.WaitGroup) {
	for downlinkTXAck := range gwbackend.Backend().DownlinkTXAckChan() {
		go func(downlinkTXAck gw.DownlinkTXAck) {
			wg.Add(1)
			defer wg.Done()

			// The ctxID will be available as context value "ctx_id" so that
			// this can be used when writing logs. This makes it easier to
			// group multiple log-lines to the same context.
			var ctxID uuid.UUID
			if downlinkTXAck.DownlinkId != nil {
				copy(ctxID[:], downlinkTXAck.DownlinkId)
			}

			ctx := context.Background()
			ctx = context.WithValue(ctx, logging.ContextIDKey, ctxID)

			if err := ack.HandleDownlinkTXAck(ctx, downlinkTXAck); err != nil {
				log.WithFields(log.Fields{
					"gateway_id": hex.EncodeToString(downlinkTXAck.GatewayId),
					"token":      downlinkTXAck.Token,
					"ctx_id":     ctxID,
				}).WithError(err).Error("uplink: handle downlink tx ack error")
			}

		}(downlinkTXAck)
	}
}

func collectUplinkFrames(ctx context.Context, uplinkFrame gw.UplinkFrame) error {
	return collectAndCallOnce(storage.RedisPool(), uplinkFrame, func(rxPacket models.RXPacket) error {
		var uplinkIDs []uuid.UUID
		for _, p := range rxPacket.RXInfoSet {
			uplinkIDs = append(uplinkIDs, helpers.GetUplinkID(p))
		}

		log.WithFields(log.Fields{
			"uplink_ids": uplinkIDs,
			"mtype":      rxPacket.PHYPayload.MHDR.MType,
			"ctx_id":     ctx.Value(logging.ContextIDKey),
		}).Info("uplink: frame(s) collected")

		// update the gateway meta-data
		if err := gateway.UpdateMetaDataInRxInfoSet(ctx, storage.DB(), storage.RedisPool(), rxPacket.RXInfoSet); err != nil {
			log.WithError(err).Error("uplink: update gateway meta-data in rx-info set error")
		}

		// log the frame for each receiving gatewa
		if err := framelog.LogUplinkFrameForGateways(ctx, storage.RedisPool(), gw.UplinkFrameSet{
			PhyPayload: uplinkFrame.PhyPayload,
			TxInfo:     rxPacket.TXInfo,
			RxInfo:     rxPacket.RXInfoSet,
		}); err != nil {
			log.WithFields(log.Fields{
				"ctx_id": ctx.Value(logging.ContextIDKey),
			}).WithError(err).Error("uplink: log uplink frames for gateways error")
		}

		// handle the frame based on message-type
		switch rxPacket.PHYPayload.MHDR.MType {
		case lorawan.JoinRequest:
			return join.Handle(ctx, rxPacket)
		case lorawan.RejoinRequest:
			return rejoin.Handle(ctx, rxPacket)
		case lorawan.UnconfirmedDataUp, lorawan.ConfirmedDataUp:
			return data.Handle(ctx, rxPacket)
		case lorawan.Proprietary:
			return proprietary.Handle(ctx, rxPacket)
		default:
			return nil
		}
	})
}
