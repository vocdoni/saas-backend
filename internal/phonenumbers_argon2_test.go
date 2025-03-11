package internal

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// BenchmarkArgon2 performs a concurrent brute-force search using Argon2.
// It simulates a scenario where the user registers with a phone number (whose hash is stored),
// and later tries to brute-force it. Progress is logged every 10 seconds.
//
// To run this benchmark, execute the following command in your terminal:
//
//	go test -v -bench=BenchmarkArgon2 -benchmem
func BenchmarkArgon2(b *testing.B) {
	// Registration: compute the hash from the phone number using a random salt.
	phone := RandomESPhoneNumber()
	b.Logf("Candidate phone number is %s", phone)
	salt := RandomBytes(32)
	startTime := time.Now()
	memory := uint32(64 * 1024)
	argonTime := uint32(4)
	argonThreads := uint8(8)

	hash := argon2.IDKey([]byte(phone), salt, argonTime, memory, argonThreads, 32)
	b.Logf("Hashed phone number in %s", time.Since(startTime))

	// Setup variables for concurrent brute-forcing.
	var totalAttempts int64        // total attempts across all workers
	var found int32                // flag to indicate result was found (0 or 1)
	const maxAttempts = 200000000  // maximum number of candidates to try
	numWorkers := runtime.NumCPU() // spawn one worker per CPU core
	b.Logf("Spawning %d workers", numWorkers)
	startTime = time.Now()

	// Create a context for cancellation once the correct phone is found.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to receive the result.
	resultChan := make(chan int, 1)
	var wg sync.WaitGroup

	// Ticker for progress reporting every 10 seconds.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(startTime)
				attempts := atomic.LoadInt64(&totalAttempts)
				hashesPerSec := float64(attempts) / elapsed.Seconds()
				// Calculate expected seconds for average-case (half the search space)
				expectedSeconds := (maxAttempts / 2.0) / hashesPerSec
				expectedDays := expectedSeconds / 86400.0
				b.Logf("Progress: %d attempts in %s (%.2f hashes/sec) - expected average time: %.2f days",
					attempts, elapsed, hashesPerSec, expectedDays)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Worker function: each worker tries a subset of candidates.
	worker := func(workerID int) {
		defer wg.Done()
		// Each worker tests candidates: workerID, workerID+numWorkers, etc.
		for i := workerID; i < maxAttempts; i += numWorkers {
			// Check if another worker already found the result.
			if atomic.LoadInt32(&found) == 1 {
				return
			}
			// Create candidate phone number.
			candidate := fmt.Sprintf("+346%d", i)
			candidateHash := argon2.IDKey([]byte(candidate), salt, argonTime, memory, argonThreads, 32)
			atomic.AddInt64(&totalAttempts, 1)
			if bytes.Equal(candidateHash, hash) {
				// We found the matching phone number.
				if atomic.CompareAndSwapInt32(&found, 0, 1) {
					resultChan <- i
					cancel() // signal cancellation to all workers and the ticker.
				}
				return
			}
		}
	}

	// Launch workers.
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go worker(w)
	}

	// Wait in a separate goroutine to close the result channel when done.
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Wait for a result (or for all workers to finish).
	if result, ok := <-resultChan; ok {
		elapsed := time.Since(startTime)
		b.Logf("Bruteforce attack successful, phone is +346%s, took %s", strconv.Itoa(result), elapsed)
	} else {
		b.Log("Bruteforce attack unsuccessful.")
	}
}

// RandomESPhoneNumber generates a randomized Spanish mobile phone number.
// Format: +34 6XXXXXXXX where X are random digits.
func RandomESPhoneNumber() string {
	const countryCode = "+34"
	const prefix = "6" // For Spanish mobile numbers.
	const numDigits = 8

	digits := make([]byte, numDigits)
	for i := 0; i < numDigits; i++ {
		// Generate a random integer in the range [0, 10)
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic(err) // In production, handle errors appropriately.
		}
		digits[i] = byte('0' + n.Int64())
	}

	return countryCode + prefix + string(digits)
}
