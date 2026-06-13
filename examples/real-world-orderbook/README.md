# Limit Order Book & Matching Engine

This repository contains the C++ implementation of a high-performance Limit Order Book and its Matching Engine.

<p align="center" width="100%">
    <img width="50%" src="demo/final_order_book.png"> 
</p>

## Overview

The Limit Order Book is designed using a combination of data structures to optimize order insertion, deletion, and matching:

### Architecture

- **Data Structures**: Orders are stored in a doubly linked-list structure, where each list represents a price level. Price levels are managed using Red-Black Trees implemented via `std::set` in C++, ensuring efficient insertion and deletion operations.

- **Order Format**: Orders are added in the format `ID, AgentID, Price, Volume`, and deleted by their id `ID`.

### Performance

To assess performance, the system was tested with 1,000,000 operations (place or delete orders), generated using a Python script. These operations were processed through a CSV parser and fed into the C++ program.

- **High-Performance**: The system achieved a peak performance of **600,000 operations per second**. Operations include order insertions, trades, and order deletions.

## Build Instructions

### Prerequisites
- CMake 3.28 or higher
- C++20 compatible compiler
- Google Test (for running tests)

### Building the Project

```bash
mkdir build
cd build
cmake ..
make
```

### Running Tests
```bash
cd build
make
./tests/Google_Tests_run
```

## Usage

The program takes two command-line arguments: an input CSV file and an output CSV file.

```bash
./OrderBook_run <input_file.csv> <output_file.csv>
```

### Input Format
The input CSV file should contain operations in the following format:
```
Operation,OrderID,AgentID,Side,Price,Volume
PLACE,1,0,0,100,50
PLACE,2,0,1,101,30
DELETE,1,,,
```

- **PLACE**: Add a new order (requires OrderID, AgentID, Side, Price, Volume)
  - Side: 0 = BUY, 1 = SELL
- **DELETE**: Remove an order (requires only OrderID)

### Output Format
The output CSV will contain the final order book state:
```
Price Limit,Side,Volume
100,BUY,50
101,SELL,30
```

### Demo
For a demonstration, refer to the [Python script](demo/generate_orders.py) used to generate sample orders. You can run it to create test data:

```bash
python demo/generate_orders.py
./OrderBook_run demo/sample_operations.csv demo/final_order_book.csv
```

## References

1. [How to Build a Fast Limit Order Book](https://web.archive.org/web/20110410160306/http://howtohft.wordpress.com:80/2011/02/15/how-to-build-a-fast-limit-order-book) - Insights into designing performant limit order books.

---

This project demonstrates a robust implementation of a Limit Order Book and Matching Engine in C++, optimized for high-frequency trading environments.
