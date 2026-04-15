package benchmark

// ParaphrasePair holds two semantically equivalent prompts.
type ParaphrasePair struct {
	Original   string
	Paraphrase string
	Category   string
}

// UnrelatedPair holds two prompts that should never match.
type UnrelatedPair struct {
	Stored string
	Query  string
}

// Paraphrases is the evaluation dataset of semantically equivalent prompt pairs.
var Paraphrases = []ParaphrasePair{
	// Question rephrases
	{"How do I reset my password?", "What are the steps to change my password?", "question-rephrase"},
	{"What is the capital of France?", "Which city serves as France's capital?", "question-rephrase"},
	{"How can I cancel my subscription?", "What is the process for ending my subscription?", "question-rephrase"},
	{"Explain quantum computing", "Give me an overview of quantum computing", "question-rephrase"},
	{"What causes climate change?", "What are the factors behind global warming?", "question-rephrase"},

	// Formal vs informal
	{"How do I install Python on my computer?", "Whats the way to get python set up on my machine?", "formal-informal"},
	{"Please describe the return policy", "Tell me about the return policy", "formal-informal"},
	{"Could you explain how photosynthesis works?", "How does photosynthesis work?", "formal-informal"},

	// Synonym swaps
	{"What are the benefits of exercise?", "What are the advantages of working out?", "synonym-swap"},
	{"How to fix a memory leak in Go?", "How to resolve a memory leak in Golang?", "synonym-swap"},
	{"Best practices for database indexing", "Recommended approaches for database indexing", "synonym-swap"},
	{"How to improve API response time?", "How to reduce API latency?", "synonym-swap"},

	// Active/passive voice changes
	{"How is authentication handled by the system?", "How does the system handle authentication?", "voice-change"},
	{"What languages are supported by the API?", "What languages does the API support?", "voice-change"},

	// Structural rephrases
	{"I need help setting up Docker containers", "Help me configure Docker containers", "structural"},
	{"Can you tell me the difference between TCP and UDP?", "What distinguishes TCP from UDP?", "structural"},
}

// UnrelatedPairs is the evaluation dataset of semantically distinct prompt pairs.
var UnrelatedPairs = []UnrelatedPair{
	{"How do I reset my password?", "What is the weather forecast for tomorrow?"},
	{"Explain quantum computing", "How to bake chocolate chip cookies"},
	{"What causes climate change?", "What is the best programming language?"},
	{"How to fix a memory leak in Go?", "What are the rules of basketball?"},
	{"Best practices for database indexing", "How to train a puppy?"},
	{"How to improve API response time?", "What is the history of the Roman Empire?"},
	{"How does photosynthesis work?", "How to file taxes in the US?"},
	{"What are the benefits of exercise?", "Explain the plot of Hamlet"},
	{"How do I install Python on my computer?", "What is the population of Japan?"},
	{"How is authentication handled by the system?", "Recommend a good Italian restaurant"},
}
