package main

import (
	"context"
	"fmt"
	"log"
	"os"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func main() {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		log.Fatal("MINIMAX_API_KEY not set")
	}

	modelID := "MiniMax-M2.7"
	if len(os.Args) > 1 {
		modelID = os.Args[1]
	}

	fmt.Printf("Testing MiniMax provider with model: %s\n", modelID)
	fmt.Println("===========================================")

	llm, err := llmproviders.InitializeLLM(llmproviders.Config{
		Provider: llmproviders.ProviderMiniMax,
		ModelID:  modelID,
	})
	if err != nil {
		log.Fatalf("Failed to initialize MiniMax LLM: %v", err)
	}

	ctx := context.Background()

	// Test 1: Simple generation
	fmt.Println("\n[Test 1] Simple generation")
	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "You are a helpful assistant. Be concise."}},
		},
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What is 2+2? Answer in one sentence."}},
		},
	}

	resp, err := llm.GenerateContent(ctx, messages)
	if err != nil {
		log.Fatalf("GenerateContent failed: %v", err)
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Response: %s\n", resp.Choices[0].Content)
		fmt.Printf("Stop reason: %s\n", resp.Choices[0].StopReason)
		if gi := resp.Choices[0].GenerationInfo; gi != nil {
			if gi.InputTokens != nil {
				fmt.Printf("Input tokens:  %d\n", *gi.InputTokens)
			}
			if gi.OutputTokens != nil {
				fmt.Printf("Output tokens: %d\n", *gi.OutputTokens)
			}
			if gi.TotalTokens != nil {
				fmt.Printf("Total tokens:  %d\n", *gi.TotalTokens)
			}
			if gi.CachedContentTokens != nil {
				fmt.Printf("Cached tokens: %d\n", *gi.CachedContentTokens)
			}
		}
	}

	// Test 2: Streaming
	fmt.Println("\n[Test 2] Streaming generation")
	streamChan := make(chan llmtypes.StreamChunk, 100)
	streamMessages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Count from 1 to 5, one number per line."}},
		},
	}

	var streamResp *llmtypes.ContentResponse
	done := make(chan error, 1)
	go func() {
		var err error
		streamResp, err = llm.GenerateContent(ctx, streamMessages, llmtypes.WithStreamingChan(streamChan))
		done <- err
	}()

	fmt.Print("Streamed: ")
	for chunk := range streamChan {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			fmt.Print(chunk.Content)
		}
	}
	fmt.Println()

	if err := <-done; err != nil {
		log.Fatalf("Streaming failed: %v", err)
	}

	if streamResp != nil && len(streamResp.Choices) > 0 {
		if gi := streamResp.Choices[0].GenerationInfo; gi != nil {
			if gi.InputTokens != nil {
				fmt.Printf("Stream input tokens:  %d\n", *gi.InputTokens)
			}
			if gi.OutputTokens != nil {
				fmt.Printf("Stream output tokens: %d\n", *gi.OutputTokens)
			}
			if gi.TotalTokens != nil {
				fmt.Printf("Stream total tokens:  %d\n", *gi.TotalTokens)
			}
			if gi.CachedContentTokens != nil {
				fmt.Printf("Stream cached tokens: %d\n", *gi.CachedContentTokens)
			}
		}
	}

	fmt.Println("\n===========================================")
	fmt.Println("All tests passed!")
}
