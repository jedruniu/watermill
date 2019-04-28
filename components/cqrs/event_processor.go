package cqrs

import (
	"context"

	"github.com/pkg/errors"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
)

// EventHandler receive event defined by NewEvent and handle it with Handle method.
// If using DDD, CommandHandler may modify and persist the aggregate.
// It can also invoke process manager, saga or just build a read model.
//
// In contrast to CommandHandler, every Event can have multiple EventHandlers.
type EventHandler interface {
	// HandlerName is named used in message.Router for creating handler.
	//
	// It will be also passed to EventsSubscriberConstructor.
	// May be useful for creating for example consumer group per handler.
	//
	// WARNING: If HandlerName was changed changed and is used for example for generating consumer groups,
	// it may result with **reconsuming all messages** !!!
	HandlerName() string

	NewEvent() interface{}

	Handle(ctx context.Context, event interface{}) error
}

// EventsSubscriberConstructor creates subscriber for EventHandler.
// It allows you to create separated customized Subscriber for every command handler.
type EventsSubscriberConstructor func(handlerName string) (message.Subscriber, error)

// EventProcessor determines which EventHandler should handle event received from event bus.
type EventProcessor struct {
	handlers      []EventHandler
	generateTopic func(eventName string) string

	subscriberConstructor EventsSubscriberConstructor

	marshaler CommandEventMarshaler
	logger    watermill.LoggerAdapter
}

func NewEventProcessor(
	handlers []EventHandler,
	generateTopic func(eventName string) string,
	subscriberConstructor EventsSubscriberConstructor,
	marshaler CommandEventMarshaler,
	logger watermill.LoggerAdapter,
) *EventProcessor {
	if len(handlers) == 0 {
		panic("missing handlers")
	}
	if generateTopic == nil {
		panic("nil generateTopic")
	}
	if subscriberConstructor == nil {
		panic("missing subscriberConstructor")
	}
	if marshaler == nil {
		panic("missing marshaler")
	}
	if logger == nil {
		logger = watermill.NopLogger{}
	}

	return &EventProcessor{
		handlers,
		generateTopic,
		subscriberConstructor,
		marshaler,
		logger,
	}
}

func (p EventProcessor) AddHandlersToRouter(r *message.Router) error {
	for i := range p.Handlers() {
		handler := p.handlers[i]
		handlerName := handler.HandlerName()
		eventName := p.marshaler.Name(handler.NewEvent())
		topicName := p.generateTopic(eventName)

		logger := p.logger.With(watermill.LogFields{
			"event_handler_name": handlerName,
			"topic":              topicName,
		})

		handlerFunc, err := p.RouterHandlerFunc(handler, logger)
		if err != nil {
			return err
		}

		logger.Debug("Adding CQRS event handler to router", nil)

		subscriber, err := p.subscriberConstructor(handlerName)
		if err != nil {
			return errors.Wrap(err, "cannot create subscriber for event processor")
		}

		r.AddNoPublisherHandler(
			handlerName,
			topicName,
			subscriber,
			handlerFunc,
		)
	}

	return nil
}

func (p EventProcessor) Handlers() []EventHandler {
	return p.handlers
}

func (p EventProcessor) RouterHandlerFunc(handler EventHandler, logger watermill.LoggerAdapter) (message.HandlerFunc, error) {
	initEvent := handler.NewEvent()
	expectedEventName := p.marshaler.Name(initEvent)

	if err := p.validateEvent(initEvent); err != nil {
		return nil, err
	}

	return func(msg *message.Message) ([]*message.Message, error) {
		event := handler.NewEvent()
		messageEventName := p.marshaler.NameFromMessage(msg)

		if messageEventName != expectedEventName {
			logger.Trace("Received different event type than expected, ignoring", watermill.LogFields{
				"message_uuid":        msg.UUID,
				"expected_event_type": expectedEventName,
				"received_event_type": messageEventName,
			})
			return nil, nil
		}

		logger.Debug("Handling event", watermill.LogFields{
			"message_uuid":        msg.UUID,
			"received_event_type": messageEventName,
		})

		if err := p.marshaler.Unmarshal(msg, event); err != nil {
			return nil, err
		}

		if err := handler.Handle(msg.Context(), event); err != nil {
			logger.Debug("Error when handling event", watermill.LogFields{"err": err})
			return nil, err
		}

		return nil, nil
	}, nil
}

func (p EventProcessor) validateEvent(event interface{}) error {
	// EventHandler's NewEvent must return a pointer, because it is used to unmarshal
	if err := isPointer(event); err != nil {
		return errors.Wrap(err, "command must be a non-nil pointer")
	}

	return nil
}
