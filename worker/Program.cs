using System;
using System.Data.Common;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;
using Newtonsoft.Json;
using Npgsql;
using StackExchange.Redis;

namespace Worker
{
    public class Program
    {
        private static readonly CancellationTokenSource cts = new CancellationTokenSource();

        public static async Task Main(string[] args)
        {
            try
            {
                // For rollout & breaking image
                Thread.Sleep(5000);
                Console.Error.WriteLine("Error in the code, blocked…");
                Thread.Sleep(10000000);

                var pgsql = OpenDbConnection();
                var redisConn = OpenRedisConnection();
                var redis = redisConn.GetDatabase();

                // Start health check server in a separate thread
                Task.Run(() => StartHealthCheckServer(cts.Token));

                // Keep alive is not implemented in Npgsql yet. This workaround was recommended:
                // https://github.com/npgsql/npgsql/issues/1214#issuecomment-235828359
                var keepAliveCommand = pgsql.CreateCommand();
                keepAliveCommand.CommandText = "SELECT 1";

                var definition = new { vote = "", voter_id = "" };

                while (!cts.Token.IsCancellationRequested)
                {
                    // Slow down to prevent CPU spike, only query each 200ms
                    await Task.Delay(200, cts.Token);

                    // Reconnect redis if down
                    if (redisConn == null || !redisConn.IsConnected)
                    {
                        Console.WriteLine("Reconnecting Redis");
                        redisConn = OpenRedisConnection();
                        redis = redisConn.GetDatabase();
                    }

                    string json = await redis.ListLeftPopAsync("votes");
                    if (json != null)
                    {
                        var vote = JsonConvert.DeserializeAnonymousType(json, definition);
                        Console.WriteLine($"Processing vote for '{vote.vote}' by '{vote.voter_id}'");
                        // Reconnect DB if down
                        if (!pgsql.State.Equals(System.Data.ConnectionState.Open))
                        {
                            Console.WriteLine("Reconnecting DB");
                            pgsql = OpenDbConnection();
                        }
                        else
                        { // Normal +1 vote requested
                            UpdateVote(pgsql, vote.voter_id, vote.vote);
                        }
                    }
                    else
                    {
                        keepAliveCommand.ExecuteNonQuery();
                    }
                }
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine(ex.ToString());
                cts.Cancel();
            }
        }

        private static async Task StartHealthCheckServer(CancellationToken token)
        {
            var listener = new HttpListener();
            listener.Prefixes.Add("http://*:8080/healthz/");
            listener.Start();
            Console.WriteLine("Health check server started at http://*:8080/healthz/");

            while (!token.IsCancellationRequested)
            {
                try
                {
                    var context = await listener.GetContextAsync();
                    context.Response.StatusCode = 200;
                    var responseMessage = "Healthy";
                    byte[] responseBytes = System.Text.Encoding.UTF8.GetBytes(responseMessage);
                    context.Response.OutputStream.Write(responseBytes, 0, responseBytes.Length);
                    context.Response.Close();
                }
                catch (HttpListenerException ex) when (ex.ErrorCode == 995) // Cancelled IO
                {
                    Console.WriteLine("Health check server shutting down.");
                    break;
                }
                catch (Exception ex)
                {
                    Console.Error.WriteLine($"Error in health check server: {ex}");
                }
            }

            listener.Close();
        }

        private static NpgsqlConnection OpenDbConnection()
        {
            string connectionString = Environment.GetEnvironmentVariable("POSTGRESQL_CONNECTION_STRING");

            if (string.IsNullOrWhiteSpace(connectionString))
            {
                throw new InvalidOperationException("Environment variable 'POSTGRESQL_CONNECTION_STRING' is not set or empty. Application cannot start.");
            }

            NpgsqlConnection connection;
            while (true)
            {
                try
                {
                    connection = new NpgsqlConnection(connectionString);
                    connection.Open();
                    break;
                }
                catch (SocketException e)
                {
                    Console.Error.WriteLine(e.ToString());
                    Console.Error.WriteLine("Waiting for db");
                    Thread.Sleep(1000);
                }
                catch (DbException e)
                {
                    Console.Error.WriteLine(e.ToString());
                    Console.Error.WriteLine("Waiting for db");
                    Thread.Sleep(1000);
                }
            }

            Console.WriteLine("Connected to db");

            var command = connection.CreateCommand();
            command.CommandText = @"CREATE TABLE IF NOT EXISTS votes (
                                        id VARCHAR(255) NOT NULL UNIQUE,
                                        vote VARCHAR(255) NOT NULL
                                    )";
            command.ExecuteNonQuery();

            return connection;
        }

        private static ConnectionMultiplexer OpenRedisConnection()
        {
            var connectionString = Environment.GetEnvironmentVariable("REDIS_CONNECTION_STRING");

            if (string.IsNullOrWhiteSpace(connectionString))
            {
                throw new InvalidOperationException("Environment variable 'REDIS_CONNECTION_STRING' is not set or empty. Application cannot start.");
            }

            ConnectionMultiplexer connection;
            while (true)
            {
                try
                {
                    connection = ConnectionMultiplexer.Connect(connectionString);
                    break;
                }
                catch (RedisConnectionException)
                {
                    Console.Error.WriteLine("Waiting for redis");
                    Thread.Sleep(1000);
                }
            }
            Console.WriteLine($"Connected to redis");
            return connection;
        }

        private static void UpdateVote(NpgsqlConnection connection, string voterId, string vote)
        {
            var command = connection.CreateCommand();
            try
            {
                command.CommandText = "INSERT INTO votes (id, vote) VALUES (@id, @vote)";
                command.Parameters.AddWithValue("@id", voterId);
                command.Parameters.AddWithValue("@vote", vote);
                command.ExecuteNonQuery();
            }
            catch (DbException)
            {
                command.CommandText = "UPDATE votes SET vote = @vote WHERE id = @id";
                command.ExecuteNonQuery();
            }
            finally
            {
                command.Dispose();
            }
        }
    }
}
