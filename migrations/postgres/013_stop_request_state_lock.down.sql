-- Restore the pre-013 guard; controlled rollback reopens the direct-writer race.
CREATE OR REPLACE FUNCTION protect_cancellation_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.status<>'REQUESTED' OR NEW.version<>1 OR NEW.kitchen_confirmer<>'' OR NEW.manual_exception_reason<>'' THEN RAISE EXCEPTION 'stop requests start requested'; END IF;
  IF NOT EXISTS(SELECT 1 FROM operation_order_state s JOIN operation_kds_cards k USING(order_id) WHERE s.order_id=NEW.order_id AND s.fulfillment_status='PREPARING') THEN RAISE EXCEPTION 'stop request requires preparing KDS card'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.request_id<>OLD.request_id OR NEW.order_id<>OLD.order_id OR NEW.line_index<>OLD.line_index OR NEW.quantity<>OLD.quantity OR NEW.reason<>OLD.reason OR NEW.requesting_manager<>OLD.requesting_manager OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'stop request identity is immutable'; END IF;
 IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'stop request version must advance once'; END IF;
 IF OLD.kitchen_confirmer<>'' AND NEW.kitchen_confirmer<>OLD.kitchen_confirmer THEN RAISE EXCEPTION 'kitchen decision is immutable'; END IF;
 IF OLD.manual_exception_reason<>'' AND NEW.manual_exception_reason<>OLD.manual_exception_reason THEN RAISE EXCEPTION 'manual exception reason is immutable'; END IF;
 IF NOT ((OLD.status='REQUESTED' AND NEW.status IN ('KITCHEN_CONFIRMED','REJECTED','WITHDRAWN')) OR
         (OLD.status='KITCHEN_CONFIRMED' AND NEW.status IN ('FINALIZED','WITHDRAWN')) OR
         (OLD.status='KITCHEN_CONFIRMED' AND NEW.status='KITCHEN_CONFIRMED' AND OLD.manual_exception_reason='' AND NEW.manual_exception_reason<>'')) THEN RAISE EXCEPTION 'invalid stop request transition'; END IF;
 IF OLD.manual_exception_reason<>'' AND NEW.status='FINALIZED' THEN RAISE EXCEPTION 'manual exception cannot finalize'; END IF;
 RETURN NEW;
END;
$$;
