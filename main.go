package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bendahl/uinput"
	"github.com/godbus/dbus"
	"github.com/mitchellh/go-homedir"
	"github.com/muesli/streamdeck"
)

var (
	deck *Deck

	dbusConn *dbus.Conn
	keyboard uinput.Keyboard

	xorg          *Xorg
	recentWindows []Window

	deckFile   = flag.String("deck", "deckmaster.deck", "path to deck config file")
	device     = flag.String("device", "", "which device to use (serial number)")
	brightness = flag.Uint("brightness", 80, "brightness in percent")
)

const (
	longPressDuration = 350 * time.Millisecond
)

func expandPath(base, path string) (string, error) {
	var err error
	path, err = homedir.Expand(path)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}

	return filepath.Abs(path)
}

func eventLoop(dev *streamdeck.Device, tch chan interface{}) {
	var keyStates sync.Map
	keyTimestamps := make(map[uint8]time.Time)

	kch, err := dev.ReadKeys()
	if err != nil {
		log.Fatal(err)
	}
	for {
		select {
		case <-time.After(100 * time.Millisecond):
			deck.updateWidgets(dev)

		case k, ok := <-kch:
			if !ok {
				err = dev.Open()
				if err != nil {
					log.Fatal(err)
				}
				continue
			}

			var state bool
			if ks, ok := keyStates.Load(k.Index); ok {
				state = ks.(bool)
			}
			keyStates.Store(k.Index, k.Pressed)

			if state && !k.Pressed {
				// key was released
				if time.Since(keyTimestamps[k.Index]) < longPressDuration {
					// log.Println("Triggering short action")
					deck.triggerAction(dev, k.Index, false)
				}
			}
			if !state && k.Pressed {
				// key was pressed
				go func() {
					// launch timer to observe keystate
					time.Sleep(longPressDuration)

					if state, ok := keyStates.Load(k.Index); ok && state.(bool) {
						// key still pressed
						// log.Println("Triggering long action")
						deck.triggerAction(dev, k.Index, true)
					}
				}()
			}
			keyTimestamps[k.Index] = time.Now()

		case e := <-tch:
			switch event := e.(type) {
			case WindowClosedEvent:
				handleWindowClosed(dev, event)

			case ActiveWindowChangedEvent:
				handleActiveWindowChanged(dev, event)
			}
		}
	}
}

func initDevice() (*streamdeck.Device, error) {
	d, err := streamdeck.Devices()
	if err != nil {
		log.Fatal(err)
	}
	if len(d) == 0 {
		return nil, fmt.Errorf("no Stream Deck devices found")
	}

	dev := d[0]
	if len(*device) > 0 {
		found := false
		for _, v := range d {
			if v.Serial == *device {
				dev = v
				found = true
				break
			}
		}
		if !found {
			log.Println("can't find device. Available devices:")
			for _, v := range d {
				log.Printf("Serial %s (%d buttons)\n", v.Serial, v.Columns*v.Rows)
			}
			os.Exit(1)
		}
	}

	if err := dev.Open(); err != nil {
		return nil, err
	}
	ver, err := dev.FirmwareVersion()
	if err != nil {
		return nil, err
	}
	log.Printf("Found device with serial %s (%d buttons, firmware %s)\n",
		dev.Serial, dev.Columns*dev.Rows, ver)

	if err := dev.Reset(); err != nil {
		return nil, err
	}

	if *brightness > 100 {
		*brightness = 100
	}
	if err = dev.SetBrightness(uint8(*brightness)); err != nil {
		return nil, err
	}

	return &dev, nil
}

func main() {
	flag.Parse()

	// initialize device
	dev, err := initDevice()
	if err != nil {
		log.Fatal(err)
	}

	// initialize dbus connection
	dbusConn, err = dbus.SessionBus()
	if err != nil {
		log.Fatal(err)
	}

	// initialize xorg connection and track window focus
	tch := make(chan interface{})
	xorg, err = Connect(os.Getenv("DISPLAY"))
	if err == nil {
		defer xorg.Close()
		xorg.TrackWindows(tch, time.Second)
	}

	// initialize virtual keyboard
	keyboard, err = uinput.CreateKeyboard("/dev/uinput", []byte("Deckmaster"))
	if err != nil {
		log.Printf("Could not create virtual input device (/dev/uinput): %s", err)
		log.Println("Emulating keyboard events will be disabled!")
	} else {
		defer keyboard.Close() //nolint:errcheck
	}

	// load deck
	deck, err = LoadDeck(dev, ".", *deckFile)
	if err != nil {
		log.Fatal(err)
	}
	deck.updateWidgets(dev)

	eventLoop(dev, tch)
}
