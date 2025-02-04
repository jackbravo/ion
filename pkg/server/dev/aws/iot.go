package aws

import (
	"log/slog"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

const BUFFER_SIZE = 1024 * 4

type IoTWriter struct {
	topic  string
	client MQTT.Client
	buffer []byte // buffer to accumulate data
}

func NewIoTWriter(client MQTT.Client, topic string) *IoTWriter {
	return &IoTWriter{
		client: client,
		buffer: make([]byte, 0, BUFFER_SIZE),
		topic:  topic,
	}
}

func (iw *IoTWriter) Write(p []byte) (int, error) {
	slog.Info("writing to topic", "topic", iw.topic, "data", string(p))
	totalWritten := 0

	for len(p) > 0 {
		// Calculate the space left in the buffer
		spaceLeft := BUFFER_SIZE - len(iw.buffer)

		// Determine how much data to copy to the buffer
		toCopy := min(spaceLeft, len(p))

		// Append data to the buffer
		iw.buffer = append(iw.buffer, p[:toCopy]...)

		// Update the slice p and totalWritten
		p = p[toCopy:]
		totalWritten += toCopy

		// If the buffer is full, write the chunk
		if len(iw.buffer) == BUFFER_SIZE {
			token := iw.client.Publish(iw.topic, 1, false, iw.buffer)
			slog.Info("waiting")
			token.Wait()
			if err := token.Error(); err != nil {
				return totalWritten, err
			}
			// Clear the buffer
			iw.buffer = iw.buffer[:0]
		}
	}

	return totalWritten, nil
}

func (iw *IoTWriter) Flush() error {
	slog.Info("flushing buffer", "topic", iw.topic)
	if len(iw.buffer) > 0 {
		token := iw.client.Publish(iw.topic, 1, false, iw.buffer)
		token.Wait()
		if err := token.Error(); err != nil {
			return err
		}
		iw.buffer = iw.buffer[:0]
	}
	return nil
}

// min returns the smaller of x or y.
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}
