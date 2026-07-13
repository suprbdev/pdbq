-- field: userById
SELECT jsonb_build_object('id', __t_1."id", 'home', CASE WHEN (__t_1."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (__t_1."address")."street", 'city', (__t_1."address")."city", 'mood', CASE (__t_1."address")."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END, '__typename', 'Address'::text) END, 'prevAddresses', CASE WHEN __t_1."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((__t_1."prev_addresses")[__e_2.o])::text IS NULL THEN NULL ELSE jsonb_build_object('city', ((__t_1."prev_addresses")[__e_2.o])."city", 'mood', CASE ((__t_1."prev_addresses")[__e_2.o])."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) END ORDER BY __e_2.o), '[]'::jsonb) FROM (SELECT generate_subscripts(__t_1."prev_addresses", 1) AS o) AS __e_2) END) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

