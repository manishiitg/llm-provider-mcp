package picli

import "testing"

func TestPiInputDeliveryModeReadiness(t *testing.T) {
	tests := []struct {
		name         string
		mode         piInputDeliveryMode
		waitForIdle  bool
		bypassBroker bool
	}{
		{name: "initial prompt", mode: piInputInitialPrompt, bypassBroker: true},
		{name: "live steer", mode: piInputLiveSteer, bypassBroker: true},
		{name: "retained turn", mode: piInputRetainedTurn, waitForIdle: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.mode.waitForIdle(); got != test.waitForIdle {
				t.Fatalf("waitForIdle() = %v, want %v", got, test.waitForIdle)
			}
			if got := test.mode.bypassBrokerReadiness(); got != test.bypassBroker {
				t.Fatalf("bypassBrokerReadiness() = %v, want %v", got, test.bypassBroker)
			}
		})
	}
}
